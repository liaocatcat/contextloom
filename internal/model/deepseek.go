package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// retryBackoff 指数退避 + 抖动：base 1s * 2^attempt，叠加 0~1s 随机抖动，
// 避免并发重试时同时打爆 API（attempt 从 0 开始）
func retryBackoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt)) * time.Second
	jitter := time.Duration(rand.Int63n(int64(time.Second)))
	return base + jitter
}

type DeepSeekBackend struct {
	endpoint   string
	apiKey     string
	modelID    string
	modelMu    sync.Mutex // 保护 modelID：识图切换（SwitchModel）与并发请求的读写安全
	maxContext int
	// maxTokens 由 maxTokens() 方法动态计算：取 maxContext，不设人为上限。
	// 用户配多大上下文（如 1M）就发多大 max_tokens——服务端若按其自身上限
	// （DeepSeek 官方 384K）拒绝，400 自修复路径会自动回退钳制重发一次，
	// 客户端不主动截断输出预算。
	client          *http.Client
	reasoningEffort string // 推理强度，以 OpenAI 兼容字段 reasoning_effort 发送（DeepSeek 若严格校验未知字段需实测调整）
	sendEffort      bool   // 是否发送 reasoning_effort（deepseek=true；openai 兼容通道=false，避免本地模型拒绝未知字段）
	streamTools     []types.ToolDefinition
	topP            *float64 // 核采样（nil=不发送，使用 API 默认值）
	temperature     *float64 // 温度（nil=不发送，使用 API 默认值）
	// supportsVision 是否支持 image_url 多模态输入。DeepSeek 官方 API 当前
	// 为纯文本模型（服务端 content 枚举只接受 text，发 image_url 直接 400），
	// 默认 false；仅当用户显式配置 model.supports_vision=true（如自建
	// OpenAI 兼容视觉模型端点）时开启。
	supportsVision bool
}

// 输出长度不限制：max_tokens 取 maxContext，按 DeepSeek 官方上限 384K 兜顶。
// 不设人为小上限（服务端默认 4096 才截断），用满上下文窗口作为输出预算。

// deepseekEffortRemap 把用户级思考深度档位映射到 API 层 reasoning_effort。
// V4.1 起 DeepSeek 原生支持 low(50)/medium/high/xhigh/max 多档；
// 此前 low 被收编到 high（三级都打 high，低档名存实亡、浪费低成本机会），
// 现按 V4.1 能力解锁低档：low 透传 low（50），medium/high 仍归一 high，
// xhigh/max 归一 max——UI 三档（low/medium/high）各归其位、名副其实。
var deepseekEffortRemap = map[string]string{
	"low":    "low",
	"medium": "high",
	"high":   "high",
	"xhigh":  "max",
	"max":    "max",
}

// maxTokens 计算单次模型输出上限：取 maxContext，不设人为上限。
// 用户配多大上下文（如 1M）就发多大 max_tokens——服务端若按其自身上限
// （DeepSeek 官方 max_tokens 上限 384K/393216）拒绝，400 自修复路径会
// 自动钳制到官方上限后重发一次（见 Chat/StreamEx 400 处理分支），
// 客户端不主动截断，把"什么上限"的裁决交给服务端。
func (d *DeepSeekBackend) maxTokens() int {
	limit := d.maxContext
	if limit < 1 {
		limit = 1
	}
	return limit
}

// maxTokensCapLadder 已知服务端 max_tokens 上限阶梯（从大到小）。
// 400 自修复解析不到显式上限时按此逐级下调：DeepSeek 官方 384K →
// glm/通用 128K → 64K。覆盖常见模型服务端，保证钳制后必然落在合法区间。
var maxTokensCapLadder = []int{393216, 131072, 65536}

// parseMaxTokensLimit 从 400 错误文本中解析服务端声明的 max_tokens 合法上限。
// 支持两种形态：
//   - "限制数值范围[1,131072]"（glm-5.3-flash）→ 131072
//   - "max_tokens must be <= 131072"（OpenAI 风格）→ 131072
//
// 解析不到返回 0，调用方退回阶梯降级。
func parseMaxTokensLimit(errText string) int {
	// 形态一：范围 [min,max] / [1,131072] / [1, 131072]
	if i := strings.Index(errText, "["); i >= 0 {
		if j := strings.Index(errText[i:], "]"); j >= 0 {
			seg := errText[i+1 : i+j]
			parts := strings.Split(seg, ",")
			if len(parts) == 2 {
				if max, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && max > 0 {
					return max
				}
			}
		}
	}
	// 形态二：<= N / < N / less than N（OpenAI 风格）
	for _, m := range []string{"<=", "less than", "<"} {
		if i := strings.Index(errText, m); i >= 0 {
			rest := strings.TrimSpace(errText[i+len(m):])
			num := 0
			for _, r := range rest {
				if r < '0' || r > '9' {
					break
				}
				num = num*10 + int(r-'0')
			}
			if num > 0 {
				return num
			}
		}
	}
	return 0
}

// nextMaxTokensCap 按已知阶梯返回比当前值更小的下一个合法上限；
// 当前值已是最低档或不在阶梯内时返回 0（表示无需继续钳制）。
func nextMaxTokensCap(cur int) int {
	for _, cap := range maxTokensCapLadder {
		if cap < cur {
			return cap
		}
	}
	return 0
}

func NewDeepSeekBackend(endpoint, apiKey, modelID string, maxContext int) *DeepSeekBackend {
	return NewDeepSeekBackendWithEffort(endpoint, apiKey, modelID, maxContext, true)
}

// NewDeepSeekBackendWithEffort 创建 DeepSeek 官方通道后端，sendEffort 控制是否
// 向 API 发送 reasoning_effort（默认 true；个别 OpenAI 兼容服务严格校验未知
// 字段时可由配置关闭）。
func NewDeepSeekBackendWithEffort(endpoint, apiKey, modelID string, maxContext int, sendEffort bool) *DeepSeekBackend {
	if apiKey == "" {
		apiKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	return &DeepSeekBackend{
		endpoint:   endpoint,
		apiKey:     apiKey,
		modelID:    modelID,
		maxContext: maxContext,
		client:     &http.Client{},
		sendEffort: sendEffort, // DeepSeek 官方通道默认发送 reasoning_effort
	}
}

// SetSendEffort 运行时热更新 reasoning_effort 发送开关（modelMu 保护）。
func (d *DeepSeekBackend) SetSendEffort(v bool) {
	d.modelMu.Lock()
	defer d.modelMu.Unlock()
	d.sendEffort = v
}

// NewOpenAICompatible 创建 OpenAI 兼容通道后端（llama.cpp / ollama / vLLM / 通用
// /v1/chat/completions 服务）。与 DeepSeek 通道的唯一差异：不发送 reasoning_effort
// 字段（本地/第三方服务常严格校验未知字段直接 400），api_key 可为空（本地模型）。
func NewOpenAICompatible(endpoint, apiKey, modelID string, maxContext int) *DeepSeekBackend {
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	return &DeepSeekBackend{
		endpoint:   endpoint,
		apiKey:     apiKey,
		modelID:    modelID,
		maxContext: maxContext,
		client:     &http.Client{},
		sendEffort: false,
	}
}

func (d *DeepSeekBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	// 发送前兜底①：目标模型不支持多模态时剥离全部 image_url（DeepSeek 官方
	// 纯文本模型发 image_url 直接 400：unknown variant 'image_url'）。
	if !d.SupportsVision() {
		messages = stripImagesForTextModel(messages)
	}
	// 发送前兜底②：超过安全红线时按预算压缩，从源头避免 400 上下文超限
	if d.maxContext > 0 && ctxmgr.NewTokenBudget(d.maxContext).SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
		if compacted, _ := ctxmgr.CompactToBudget(messages, ctxmgr.NewTokenBudget(d.maxContext)); compacted != nil {
			messages = compacted
		}
	}
	body := map[string]any{
		"model":      d.currentModel(),
		"messages":   messages,
		"max_tokens": d.maxTokens(),
	}
	if d.topP != nil {
		body["top_p"] = *d.topP
	}
	if d.temperature != nil {
		body["temperature"] = *d.temperature
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if d.sendEffort && d.reasoningEffort != "" {
		body["reasoning_effort"] = d.reasoningEffort
	}
	data, _ := json.Marshal(body)

	// 流式请求重试（借鉴业界主流桌面编码 Agent 的重试策略：超时/网络错误重试，400 不重试）
	maxRetries := 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", d.endpoint+"/chat/completions", bytes.NewReader(data))
		if err != nil {
			return "", nil, err
		}
		req.Header.Set("Authorization", "Bearer "+d.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := d.client.Do(req)
		if err != nil {
			lastErr = err
			// 网络错误：重试（指数退避+抖动）
			if attempt < maxRetries-1 {
				time.Sleep(retryBackoff(attempt))
				continue
			}
			return "", nil, fmt.Errorf("模型调用失败(重试%d次): %w", maxRetries, err)
		}

		// 400 错误不重试（消息结构问题），但自修复：优先按服务端上限钳制
		// max_tokens，其次清理非法消息 + 按预算压缩长度后重试（受 maxRetries 限制）。
		if resp.StatusCode == 400 {
			var errResult map[string]any
			json.NewDecoder(resp.Body).Decode(&errResult)
			resp.Body.Close()
			errMsg := fmt.Sprintf("API 400 错误: %v", errResult["error"])
			// ① max_tokens 超服务端上限：客户端按 maxContext 直发（用户配 1M 就发
			// 1M，不主动设人为上限）；若服务端按自身上限拒绝——如 glm-5.3-flash
			// 上限 131072（错误信息 "限制数值范围[1,131072]"）、DeepSeek 官方 384K
			// ——解析错误信息中的合法上限优先钳制，解析不到则按已知阶梯逐级下调
			// 重发（每次 400 都允许继续降级，直至 maxRetries 用尽），请求不丢。
			if mt, ok := body["max_tokens"].(int); ok {
				limit := parseMaxTokensLimit(errMsg)
				if limit == 0 {
					limit = nextMaxTokensCap(mt)
				}
				if limit > 0 && mt > limit {
					body["max_tokens"] = limit
					data, _ = json.Marshal(body)
					continue
				}
			}
			// ② 消息结构/长度问题：仅首次 400 清洗重试一次（幂等清洗，语义与历史一致）
			if attempt == 0 {
				messages = sanitizeForAPI(messages)
				// 多模态兜底：即使 supports_vision 配置有误，400 重试也强制
				// 剥离 image_url（unknown variant 'image_url' 不可能通过清洗修复）
				messages = stripImagesForTextModel(messages)
				// 长度兜底：若 400 源于上下文超限，按 max_context 预算压缩消息后重发
				if d.maxContext > 0 {
					b := ctxmgr.NewTokenBudget(d.maxContext)
					if compacted, changed := ctxmgr.CompactToBudget(messages, b); changed {
						messages = compacted
					}
				}
				body["messages"] = messages
				data, _ = json.Marshal(body)
				continue
			}
			return "", nil, fmt.Errorf("%s", errMsg)
		}

		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			lastErr = err
			if attempt < maxRetries-1 {
				continue
			}
			return "", nil, err
		}
		resp.Body.Close()

		content := extractContent(result)
		toolCalls := extractToolCalls(result)
		// 兜底：API 未返回结构化 tool_calls 时，从文本/XML 内容解析工具调用，
		// 防止"模型以文本表达调用意图 → 直接打印、从未执行"。
		if len(toolCalls) == 0 {
			toolCalls = ExtractToolCallsFromText(content)
		}
		return content, toolCalls, nil
	}
	return "", nil, lastErr
}

// sanitizeForAPI API 请求前的消息清理（移除可能导致 400 的消息）
func sanitizeForAPI(messages []types.Message) []types.Message {
	var result []types.Message
	for _, msg := range messages {
		// 跳过 "context" role（非标准 role 会导致 API 400）
		if msg.Role == "context" {
			// 转换为 user role
			msg.Role = "user"
		}
		// 确保 content 不为 nil
		if msg.Content == nil {
			msg.Content = ""
		}
		result = append(result, msg)
	}
	return result
}

// StreamChunk 流式响应块
type StreamChunk struct {
	Content    string
	ToolCalls  []types.ToolCall
	IsToolCall bool
	// FinishReason 流结束原因（stop/tool_calls/length）。length=模型输出被
	// max_tokens 截断（长思考耗尽预算后工具调用被腰斩）——agent 据此不把
	// 截断的半截回复当"最终回答"收尾，而是提示模型继续。
	FinishReason string
}

// StreamEx 增强流式：返回 content 和 tool_calls
func (d *DeepSeekBackend) StreamEx(ctx context.Context, messages []types.Message) (<-chan StreamChunk, <-chan error) {
	ch := make(chan StreamChunk, 256)
	errCh := make(chan error, 1)

	// 发送前兜底①：目标模型不支持多模态时剥离全部 image_url（DeepSeek 官方
	// 纯文本模型发 image_url 直接 400：unknown variant 'image_url'）。
	if !d.SupportsVision() {
		messages = stripImagesForTextModel(messages)
	}
	// 发送前兜底②：超过安全红线时按预算压缩
	if d.maxContext > 0 && ctxmgr.NewTokenBudget(d.maxContext).SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
		if compacted, _ := ctxmgr.CompactToBudget(messages, ctxmgr.NewTokenBudget(d.maxContext)); compacted != nil {
			messages = compacted
		}
	}

	// 构建请求 body（在 goroutine 外完成，避免重复构建）
	body := map[string]any{
		"model":      d.currentModel(),
		"messages":   messages,
		"stream":     true,
		"max_tokens": d.maxTokens(),
	}
	if d.topP != nil {
		body["top_p"] = *d.topP
	}
	if d.temperature != nil {
		body["temperature"] = *d.temperature
	}
	if len(d.streamTools) > 0 {
		body["tools"] = d.streamTools
	}
	if d.sendEffort && d.reasoningEffort != "" {
		body["reasoning_effort"] = d.reasoningEffort
	}
	data, _ := json.Marshal(body)

	go func() {
		defer close(ch)
		defer close(errCh)

		// 流式状态局部化，避免实例字段跨请求/并发数据竞争
		inReasoning := false
		var toolCallBuf []types.ToolCall

		// ── 文本工具调用兜底（跨 token 分片稳定检测 + 缓冲）──
		// 真实流式逐 token 输出会把 <invoke 切成 "<in"+"voke"、把 <|/invoke 切成
		// "<|/inv"+"oke"；对单个 chunk 做 strings.Contains 永远不命中 → 调用被当
		// 正文打印、从不执行，模型看到无结果反馈就重复输出同一调用 → 死循环。
		// 修复（三态流处理）：
		//  · 安全正文：win 尾部无标记碎片 → 立即输出并清空 win（不延迟、不重复）；
		//  · 起始标记：win 拼合碎片后命中 <invoke/<tool_calls/<parameter 等 →
		//    前缀正文先输出、调用块从标记起进入 pending 缓冲；
		//  · 壳尾标记：pending 外出现 </tool_calls/</invoke 等 → 丢弃壳尾，
		//    壳尾之后的正文保留（调用块已解析，壳不是用户内容）；
		//  · 疑似碎片：win 尾部含 "<"/"["/"`"（可能是切碎的标记）→ 保留等待；
		//  · pending 内：全文累积，endDetect 命中（基于全文，天然跨 chunk）
		//    或流末统一 flush 解析。
		var pendingMode bool
		var pendingText strings.Builder
		var win strings.Builder // 跨 chunk 拼合起始标记的滚动窗口

		// startIdx 返回内容中第一个起始标记的位置（-1=无）。
		// 注意顺序：带 DSML| 中缀的变体在前，<|invoke 在 <invoke 前
		// （避免 <|inv 被 <in… 误判位置）。
		startIdx := func(s string) int {
			for _, m := range []string{"<|DSML|invoke", "<|DSML|tool_calls", "<|DSML|parameter", "<|invoke", "<|tool_calls", "<invoke", "<tool_calls", "<parameter", "[tool_call_", "```tool_call"} {
				if i := strings.Index(s, m); i >= 0 {
					return i
				}
			}
			return -1
		}
		// 壳尾标记清单（调用块已被提取后，剩余 </tool_calls> 等纯壳不是用户内容）。
		// 必须用完整闭合标记（带 > / |>）：若用 " </invoke" 这类不带 > 的前缀，
		// 会在闭合标记的 ">" 到达前就把 ">" 当正文输出，正文残留孤立的 ">"。
		shellEndMarks := []string{"</|DSML|invoke|>", "</|DSML|tool_calls|>", "<|/invoke|>", "</|invoke|>", "</invoke>", "<|/tool_calls|>", "</|tool_calls|>", "</tool_calls>"}
		// 安全：窗口尾部是否绝不含标记碎片（含 "<"/"["/"`" 即保守保留等待）
		safeTail := func(s string) bool {
			if len(s) > 64 {
				s = s[len(s)-64:]
			}
			return !strings.ContainsAny(s, "<[`")
		}
		// 调用块结束检测。必须检测完整闭合标记（带 > / |>）：若只检测 " </invoke"
		// 前缀，会在 "</invoke>" 的 ">" 到达前触发 flush，pending 缺 ">"，解析正则
		// 匹配不到完整 "</invoke>" → 解析 0 个调用 → 整个 XML 块被当正文输出。
		// 这是"模型输出文本工具调用却从不执行"的直接根因。
		//
		// 只以 <tool_calls> 壳的完整闭合 </tool_calls>（及 DSML/围栏变体）作为
		// flush 触发点：若以 </invoke> 触发，分片 "</invoke>\n</tool_calls>" 同 chunk
		// 到达时 "</tool_calls>" 的开头 "<" 会被提前卷进 pending，flush 后该 "<" 与
		// 后续 "/tool_calls>" 碎片被当安全正文输出 → 正文残留工具调用壳。
		// 无 <tool_calls> 壳的裸 <invoke> 块由 finish_reason/流末 flushPending 兜底。
		endDetect := func() bool {
			s := pendingText.String()
			return strings.Contains(s, "</tool_calls>") ||
				strings.Contains(s, "</|DSML|tool_calls|>") ||
				strings.Contains(s, "<|/tool_calls|>") || strings.Contains(s, "</|tool_calls|>") ||
				strings.Contains(s, "```")
		}
		flushPending := func() bool {
			if pendingText.Len() == 0 {
				pendingMode = false
				return false
			}
			body := pendingText.String()
			pendingText.Reset()
			pendingMode = false
			calls := ExtractToolCallsFromText(body)
			if len(calls) > 0 {
				ch <- StreamChunk{ToolCalls: calls, IsToolCall: true}
				// 调用块之外的正文原样输出（调用块已被解析层精确提取，
				// 剩余内容若为普通正文/思考，仍应展示给用户，避免吞内容）
				cleaned := stripTextToolBlocks(body)
				if cleaned != "" {
					ch <- StreamChunk{Content: cleaned}
				}
				return true
			}
			// 不是工具调用：把缓冲正文原样输出（延迟显示，避免吞掉内容）
			ch <- StreamChunk{Content: body}
			return false
		}

		maxRetries := 3
		var lastErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}

			req, err := http.NewRequestWithContext(ctx, "POST", d.endpoint+"/chat/completions", bytes.NewReader(data))
			if err != nil {
				errCh <- err
				return
			}
			req.Header.Set("Authorization", "Bearer "+d.apiKey)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")

			resp, err := d.client.Do(req)
			if err != nil {
				lastErr = err
				if attempt < maxRetries-1 {
					time.Sleep(time.Duration(attempt+1) * time.Second)
					continue
				}
				errCh <- fmt.Errorf("API请求失败(重试%d次): %w", maxRetries, err)
				return
			}

			// 非 200：5xx 重试；400 自修复（优先按服务端上限钳制 max_tokens，
			// 其次清理非法消息 + 按预算压缩长度）后重试；其他立即报错
			if resp.StatusCode != http.StatusOK {
				errBody, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 400 {
					// ① max_tokens 超服务端上限：客户端按 maxContext 直发（用户配
					// 1M 就发 1M，不主动设人为上限）；若服务端按自身上限拒绝——
					// 如 glm-5.3-flash 上限 131072（错误信息 "限制数值范围[1,131072]"）、
					// DeepSeek 官方 384K——解析错误信息中的合法上限优先钳制，解析
					// 不到则按已知阶梯逐级下调重发，请求不丢。
					if mt, ok := body["max_tokens"].(int); ok {
						limit := parseMaxTokensLimit(string(errBody))
						if limit == 0 {
							limit = nextMaxTokensCap(mt)
						}
						if limit > 0 && mt > limit {
							body["max_tokens"] = limit
							data, _ = json.Marshal(body)
							continue
						}
					}
					if attempt == 0 {
						// ② 长度兜底：上下文超限时按 max_context 预算压缩后重发
						messages = sanitizeForAPI(messages)
						if d.maxContext > 0 {
							b := ctxmgr.NewTokenBudget(d.maxContext)
							if compacted, changed := ctxmgr.CompactToBudget(messages, b); changed {
								messages = compacted
							}
						}
						body["messages"] = messages
						data, _ = json.Marshal(body)
						continue
					}
				}
				if resp.StatusCode >= 500 && attempt < maxRetries-1 {
					lastErr = fmt.Errorf("API %d: %s", resp.StatusCode, string(errBody))
					time.Sleep(retryBackoff(attempt))
					continue
				}
				errCh <- fmt.Errorf("API %d 错误: %s", resp.StatusCode, string(errBody))
				return
			}

			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for scanner.Scan() {
				select {
				case <-ctx.Done():
					resp.Body.Close()
					return
				default:
				}
				line := scanner.Text()
				if line == "" || line == "data: [DONE]" {
					continue
				}
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				jsonStr := strings.TrimPrefix(line, "data: ")
				var chunk map[string]any
				if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
					continue
				}

				choices, ok := chunk["choices"].([]any)
				if !ok || len(choices) == 0 {
					continue
				}
				choice, ok := choices[0].(map[string]any)
				if !ok {
					continue
				}
				msg, _ := choice["delta"].(map[string]any)
				if msg == nil {
					msg, _ = choice["message"].(map[string]any)
				}
				if msg == nil {
					continue
				}

				// 分别提取 reasoning_content 和 content delta
				rc, hasRC := msg["reasoning_content"].(string)
				c, hasC := msg["content"].(string)

				// 检查 finish_reason——必须在 content/reasoning 处理【之前】：
				// 同一 chunk 常同时携带 content delta 与 finish_reason（如
				// 短回复一个 chunk 结束）。旧实现把检查放在 content 处理之后，
				// content 分支的 safeTail 输出 / pending 提前 continue 会把
				// finish_reason 跳过 → 流正常结束却收不到 stop 信号 → 走 eof
				// 兜底误判"截断"→ agent 反复重发完整请求（含 MB 级 base64
				// 图片）→ 请求体膨胀超限 / 图片识别"永远在继续"。
				if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
					if inReasoning {
						inReasoning = false
						ch <- StreamChunk{Content: "<|/reasoning|>"}
					}
					// finish_reason 收尾统一 flush：win 是跨 chunk 拼合标记碎片的
					// 滚动窗口，尾部 64 字节含 < / [ / ` 即被保守扣留（疑似切碎的
					// 标记）。流异常断开（eof）路径有 win→pending 兜底，但正常结束
					// （stop/tool_calls/length）此前只 flush pendingText，win 残留
					// 直接丢弃——两个用户可见症状的共同根因：
					//   ① 回复结尾是代码围栏 ``` / [引用] / <标签> → 尾部碎片扣在
					//      win → 最后几个字/一两行"被截断不显示"；
					//   ② 全文密集含 ` / < / [ 的回复（代码讨论类）每个分片尾部都
					//      不 safe → 整条回复滞留 win → stop 时整条丢弃 → 正文空。
					// 与 eof 兜底同规则：win 并入 pendingText 后统一 flush。
					finalFlush := func() {
						if win.Len() > 0 {
							pendingText.WriteString(win.String())
							win.Reset()
						}
						flushPending()
					}
					// 同 chunk 的 content 可能还没被处理：先按正常流输出它，
					// 再 flush 结束（不丢尾部正文）。
					if hasC && c != "" {
						if inReasoning {
							inReasoning = false
							ch <- StreamChunk{Content: "<|/reasoning|>"}
						}
						ch <- StreamChunk{Content: c}
					}
					if fr == "tool_calls" {
						if len(toolCallBuf) > 0 {
							ch <- StreamChunk{
								ToolCalls:  toolCallBuf,
								IsToolCall: true,
							}
							toolCallBuf = nil
						}
						// 文本兜底：正文中可能还有 <invoke>/[tool_call] 形式的调用
						finalFlush()
						resp.Body.Close()
						return
					}
					if fr == "stop" {
						finalFlush()
						resp.Body.Close()
						return
					}
					// finish_reason=length：模型输出被 max_tokens 截断（典型：
					// 长 reasoning 耗尽预算后工具调用 XML 腰斩）。flushPending
					// 兜底解析已收到的调用，并显式把截断信号传给 agent——
					// agent 据此不把半截回复当最终回答收尾，而是提示模型继续。
					finalFlush()
					ch <- StreamChunk{FinishReason: fr}
					resp.Body.Close()
					return
				}

				if hasRC && rc != "" {
					if !inReasoning {
						inReasoning = true
						ch <- StreamChunk{Content: "<|reasoning|>"}
					}
					ch <- StreamChunk{Content: rc}
				}
				if hasC && c != "" {
					if inReasoning {
						inReasoning = false
						ch <- StreamChunk{Content: "<|/reasoning|>"}
					}
					if pendingMode {
						// pending 内：全文累积，endDetect 命中或流末统一 flush
						pendingText.WriteString(c)
						if pendingText.Len() > 64*1024 {
							flushPending()
						} else if endDetect() {
							flushPending()
						}
						continue
					}
					// pending 外：三态判定
					win.WriteString(c)
					w := win.String()

					// 壳尾标记（调用块已解析后，剩余 </tool_calls> 等纯壳不是用户内容）
					shellIdx := -1
					for _, m := range shellEndMarks {
						if i := strings.Index(w, m); i >= 0 {
							shellIdx = i
							break
						}
					}
					// 起始标记（进入 pending 缓冲）
					si := startIdx(w)
					if si >= 0 {
						// 前缀正文先输出（调用块从标记起进 pending）
						prefix := w[:si]
						if prefix != "" && safeTail(prefix) {
							ch <- StreamChunk{Content: prefix}
						}
						pendingMode = true
						pendingText.Reset()
						pendingText.WriteString(w[si:])
						win.Reset()
						if endDetect() {
							flushPending()
						}
						continue
					}
					// 壳尾与正文混合：丢弃壳尾（含壳尾前的空白），保留其后正文
					if shellIdx >= 0 {
						// 找到壳尾结束位置：从 shellIdx 向后找到 ">" 再+1
						after := shellIdx
						for after < len(w) && w[after] != '>' {
							after++
						}
						if after < len(w) {
							after++ // 跳过 '>'
						}
						// 壳尾之后可能有普通正文 → 送入窗口等待（壳后的 "<" 是新的标记候选）
						rest := w[after:]
						win.Reset()
						if rest != "" {
							win.WriteString(rest)
						}
						continue
					}
					// 安全正文：窗口尾部无标记碎片 → 立即输出并清空窗口
					if safeTail(w) {
						ch <- StreamChunk{Content: w}
						win.Reset()
						continue
					}
					// 疑似碎片：窗口保留等待下个 chunk 拼合（不输出）
				}

				// 提取 tool_calls (流式 delta 增量累积)
				if rawCalls, ok := msg["tool_calls"].([]any); ok {
					for _, callItem := range rawCalls {
						callMap, ok := callItem.(map[string]any)
						if !ok {
							continue
						}
						idx := 0
						if i, ok := callMap["index"].(float64); ok {
							idx = int(i)
						}
						fn, _ := callMap["function"].(map[string]any)
						if idx >= len(toolCallBuf) {
							toolCallBuf = append(toolCallBuf, types.ToolCall{})
						}
						if id, ok := callMap["id"].(string); ok && id != "" {
							toolCallBuf[idx].ID = id
						}
						if t, ok := callMap["type"].(string); ok && t != "" {
							toolCallBuf[idx].Type = t
						}
						if fn != nil {
							if name, ok := fn["name"].(string); ok && name != "" {
								toolCallBuf[idx].Function.Name = name
							}
							if args, ok := fn["arguments"].(string); ok {
								toolCallBuf[idx].Function.Arguments += args
							}
						}
					}
				}
			}
			// 流结束兜底（走到这里 = 未收到任何 finish_reason 就断流：
			// 连接中断/网关超时/服务端异常关闭）。flushPending 兜底解析已收到
			// 的调用，并显式发 FinishReason="eof" 截断标记——agent 据此知道
			// 这是非正常结束（半截回复），不把它当最终回答收尾。
			if inReasoning {
				inReasoning = false
				ch <- StreamChunk{Content: "<|/reasoning|>"}
			}
			// win 里可能残留未拼合的标记碎片或正文，并入 pending 统一 flush
			if win.Len() > 0 {
				pendingText.WriteString(win.String())
				win.Reset()
			}
			flushPending()
			ch <- StreamChunk{FinishReason: "eof"}
			if err := scanner.Err(); err != nil {
				errCh <- err
			}
			resp.Body.Close()
			return // 成功读完，不重试
		}
		if lastErr != nil {
			errCh <- lastErr
		}
	}()

	return ch, errCh
}

func (d *DeepSeekBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 256)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		defer close(errCh)

		streamCh, streamErrCh := d.StreamEx(ctx, messages)
		for {
			select {
			case sc, ok := <-streamCh:
				if !ok {
					// 流已读完（正常或错误，错误已通过 errCh 传递），结束转发
					return
				}
				if sc.Content != "" {
					ch <- sc.Content
				}
			case err, ok := <-streamErrCh:
				if ok && err != nil {
					errCh <- err
					return
				}
				// errCh 正常关闭（ok=false）不代表流读完：继续消费 streamCh 剩余 chunk
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, errCh
}

// currentModel 加锁读取当前模型 ID（识图切换与并发请求安全）。
func (d *DeepSeekBackend) currentModel() string {
	d.modelMu.Lock()
	defer d.modelMu.Unlock()
	return d.modelID
}

func (d *DeepSeekBackend) ModelID() string {
	return d.currentModel()
}

func (d *DeepSeekBackend) MaxContext() int {
	return d.maxContext
}

// SwitchModel 切换模型 ID（识图模式：本轮切换到视觉模型，结束后恢复原模型）。
// 与请求构造（currentModel）互斥，避免并发 Agent 场景下读到半切换状态。
func (d *DeepSeekBackend) SwitchModel(modelID string) {
	d.modelMu.Lock()
	d.modelID = modelID
	d.modelMu.Unlock()
}

// SetSupportsVision 设置多模态输入能力标志（config model.supports_vision）。
func (d *DeepSeekBackend) SetSupportsVision(v bool) {
	d.modelMu.Lock()
	d.supportsVision = v
	d.modelMu.Unlock()
}

// SetEndpoint 热更新 API 端点（GUI 保存 model.endpoint 后立即生效，
// 供 Runtime.applyLiveConfig 调用；与请求构造互斥，避免读到半切换状态）。
func (d *DeepSeekBackend) SetEndpoint(endpoint string) {
	d.modelMu.Lock()
	d.endpoint = endpoint
	d.modelMu.Unlock()
}

// SetAPIKey 热更新 API Key（GUI 保存 model.api_key 后立即生效；
// 传入空串时保持现状，避免误清空）。
func (d *DeepSeekBackend) SetAPIKey(key string) {
	if key == "" {
		return
	}
	d.modelMu.Lock()
	d.apiKey = key
	d.modelMu.Unlock()
}

// SetMaxContext 热更新上下文窗口上限（GUI 保存 model.max_context 后立即生效）。
func (d *DeepSeekBackend) SetMaxContext(maxContext int) {
	d.modelMu.Lock()
	d.maxContext = maxContext
	d.modelMu.Unlock()
}

// SupportsVision 报告是否支持 image_url 多模态输入（与 SwitchModel 共用锁，
// 保证读到的能力标志与模型 ID 一致，无半切换状态）。
func (d *DeepSeekBackend) SupportsVision() bool {
	d.modelMu.Lock()
	defer d.modelMu.Unlock()
	return d.supportsVision
}

// stripImagesForTextModel 剥离消息中全部 image_url 片段（目标模型纯文本时的
// 发送前兜底）：任何路径（历史透传/JSONL 恢复/检查点/子 Agent 继承）漏网的
// 多模态消息在此统一降级为纯文本占位，保证 API 永不收到 image_url。
func stripImagesForTextModel(messages []types.Message) []types.Message {
	changed := false
	out := make([]types.Message, len(messages))
	for i, m := range messages {
		if stripped, ok := m.WithoutImages(); ok {
			out[i] = stripped
			changed = true
		} else {
			out[i] = m
		}
	}
	if !changed {
		return messages
	}
	return out
}

// SetStreamTools sets tools to be included in stream requests
func (d *DeepSeekBackend) SetStreamTools(tools []types.ToolDefinition) {
	d.streamTools = tools
}

func (d *DeepSeekBackend) SetReasoningEffort(level string) {
	if remapped, ok := deepseekEffortRemap[level]; ok {
		d.reasoningEffort = remapped
	} else {
		d.reasoningEffort = level
	}
}

func (d *DeepSeekBackend) ReasoningEffort() string {
	return d.reasoningEffort
}

func (d *DeepSeekBackend) SetTopP(p float64) {
	d.topP = &p
}

func (d *DeepSeekBackend) TopP() float64 {
	if d.topP == nil {
		return 0
	}
	return *d.topP
}

func (d *DeepSeekBackend) SetTemperature(t float64) {
	d.temperature = &t
}

func (d *DeepSeekBackend) Temperature() float64 {
	if d.temperature == nil {
		return 0
	}
	return *d.temperature
}

type ExpertHint struct {
	Field string `json:"expert_hint"`
	Value string `json:"value"`
}

// extractToolCalls 从 DeepSeek/OpenAI 响应中提取 tool_calls 数组
func extractToolCalls(result map[string]any) []types.ToolCall {
	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil
	}
	msg, ok := choice["message"].(map[string]any)
	if !ok {
		return nil
	}
	rawCalls, ok := msg["tool_calls"].([]any)
	if !ok {
		return nil
	}
	var calls []types.ToolCall
	for _, rc := range rawCalls {
		callMap, ok := rc.(map[string]any)
		if !ok {
			continue
		}
		id, _ := callMap["id"].(string)
		typ, _ := callMap["type"].(string)
		fn, _ := callMap["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		args, _ := fn["arguments"].(string)
		calls = append(calls, types.ToolCall{
			ID:   id,
			Type: typ,
			Function: types.FunctionCall{
				Name:      name,
				Arguments: args,
			},
		})
	}
	return calls
}

// ── 文本/XML 形式工具调用解析 ──
// 某些模型或网关代理不以 OpenAI 结构化 tool_calls 返回工具调用，而是直接在
// 正文输出文本（如 "[tool_call_xxx] exec_command: ..." 或 "```tool_call ...```"）。
// 若只依赖结构化解析，这类调用会被当成"最终回答"打印出来、从不执行——
// 这里提供兜底：扫描响应正文，识别合法工具名 + 参数片段，转为 ToolCall。
//
// 支持的文本格式（按优先级）：
//  1. [tool_call_<id>] <name>: <args>     —— 工具调用行（id 可为任意小写字母数字）
//  2. ```tool_call  <name>: <args> ```    —— 围栏代码块
//  3. <invoke name="<name>"> 参数 JSON 或 key=value  </invoke>  —— XML 风格
//  4. <|invoke name="<name>"|> 参数 <|/invoke|> —— DSML 竖线风格
//     （DeepSeek 等模型实际输出 <|tool_calls|>/<|invoke|>/<|parameter|>，
//     纯 XML 正则无法匹配 → 调用从不执行，模型重复输出调用陷入死循环）
//  5. 独立行: <name>: <args> 且 <name> 命中已注册工具名
var toolCallLineRe = regexp.MustCompile(`(?m)\[tool_call_[a-z0-9_\-\]]+\]\s*([A-Za-z_][A-Za-z0-9_.]*)\s*[:：]\s*([^\n]+)`)
var toolCallFenceRe = regexp.MustCompile("(?s)```tool_call\\s*\n?([A-Za-z_][A-Za-z0-9_.]*)\\s*[:：]\\s*(.*?)```")
var toolCallXMLRe = regexp.MustCompile(`(?s)<invoke\s+name="([A-Za-z_][A-Za-z0-9_.]*)"[^>]*>(.*?)</invoke>`)
var xmlParamRe = regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)"[^>]*>(.*?)</parameter>`)

// DSML 竖线风格：<|invoke name="x"|>body<|/invoke|> 与 </|invoke|> 两种收尾写法。
// 注意：真实模型还输出 <|DSML|invoke ...>（带 DSML| 中缀，收尾为 </|DSML|invoke>），
// 因此开始标记用可选中缀 (?:DSML\|)?，收尾兼容 </|DSML|invoke> / <|/invoke|> / </invoke>。
// 真实分片流可能截断收尾（缺 |>/末尾换行），因此用可选收尾 `|>`+首个
// 非贪婪结束标记；若无法匹配完整块，稍后由流末 flushPending 兜底解析。
var dsmlInvokeRe = regexp.MustCompile(`(?s)<\|(?:DSML\|)?invoke\s+(?:name=)?"?([A-Za-z_][A-Za-z0-9_.]*)"?[^>]*?(?:\|>|>)(.*?)(?:<\|/invoke\|?>|</\|(?:DSML\|)?invoke\|?>|</invoke\|?>)`)
var dsmlParamRe = regexp.MustCompile(`(?s)<\|(?:DSML\|)?parameter\s+(?:name=)?"?([^">]+)"?[^>]*?(?:\|>|>)(.*?)(?:<\|/parameter\|?>|</\|(?:DSML\|)?parameter\|?>|</parameter\|?>)`)

// xmlParamsToJSON 把 XML <parameter name="x">值</parameter> 子元素形式归一化为 JSON 参数串。
// 未命中子元素形态时原样返回（兼容 JSON / key=value 形态）。
func xmlParamsToJSON(body string) (string, bool) {
	ms := xmlParamRe.FindAllStringSubmatch(body, -1)
	if len(ms) == 0 {
		return body, false
	}
	m := make(map[string]any, len(ms))
	for _, sm := range ms {
		m[sm[1]] = strings.TrimSpace(sm[2])
	}
	b, err := json.Marshal(m)
	if err != nil {
		return body, false
	}
	return string(b), true
}

// knownToolNames 文本兜底识别受信任的工具名集合（与 registry 内置工具一致，
// 避免把正文里的普通 "name: value" 误当成工具调用）。
var knownToolNames = map[string]bool{
	"read_file": true, "write_file": true, "replace_in_file": true,
	"exec_command": true, "search_content": true, "list_files": true,
	"run_tests": true, "git_diff": true, "web_fetch": true,
	"spawn_task": true, "send_message": true, "check_messages": true,
	"propose_vocab": true, "handoff": true, "run_toolchain": true,
	"harness_search": true,
}

// truncatedTagPairs 截断补全用的标签对（XML 与 DSML 形态的开放/闭合写法）
var truncatedTagPairs = []struct {
	opens    []string
	closes   []string
	closeTag string
}{
	{
		opens:    []string{"<parameter", "<|parameter", "<|DSML|parameter"},
		closes:   []string{"</parameter", "</|parameter", "<|/parameter", "</|DSML|parameter"},
		closeTag: "</parameter>",
	},
	{
		opens:    []string{"<invoke", "<|invoke", "<|DSML|invoke"},
		closes:   []string{"</invoke", "</|invoke", "<|/invoke", "</|DSML|invoke"},
		closeTag: "</invoke>",
	},
	{
		opens:    []string{"<tool_calls", "<|tool_calls", "<|DSML|tool_calls"},
		closes:   []string{"</tool_calls", "</|tool_calls", "<|/tool_calls", "</|DSML|tool_calls"},
		closeTag: "</tool_calls>",
	},
}

// hasUnclosedToolCall 文本中是否存在未闭合的工具调用标签（开放数 > 闭合数）。
// 用于判断是否值得做截断补全解析。
func hasUnclosedToolCall(s string) bool {
	if !strings.Contains(s, "<invoke") && !strings.Contains(s, "<|invoke") &&
		!strings.Contains(s, "<tool_calls") && !strings.Contains(s, "<|tool_calls") &&
		!strings.Contains(s, "[tool_call") && !strings.Contains(s, "```tool_call") {
		return false
	}
	for _, p := range truncatedTagPairs {
		oc, cc := 0, 0
		for _, o := range p.opens {
			oc += strings.Count(s, o)
		}
		for _, c := range p.closes {
			cc += strings.Count(s, c)
		}
		if oc > cc {
			return true
		}
	}
	return false
}

// repairTruncatedToolCall 补全被截断的工具调用块缺失的闭合标签。
// 真实场景：模型在输出 </parameter>/</invoke>/</tool_calls> 之前达到 max_tokens
// 或连接被中断，流式/非流式文本兜底都解析出 0 个调用 → agent 误判"模型未调用
// 工具"直接收尾 → 对话中止、工具从不执行（"工作完成"但实际什么都没做）。
// 按 parameter → invoke → tool_calls 由内到外统计开放/闭合标签数差并补全，
// 同时兼容 XML（<invoke>/<parameter>）与 DSML（<|invoke|>/<|parameter|>）形态。
// 无法确定截断（无任何工具调用标记）时原样返回。
func repairTruncatedToolCall(s string) string {
	if !hasUnclosedToolCall(s) {
		return s
	}
	repaired := s
	for _, p := range truncatedTagPairs {
		oc, cc := 0, 0
		for _, o := range p.opens {
			oc += strings.Count(repaired, o)
		}
		for _, c := range p.closes {
			cc += strings.Count(repaired, c)
		}
		for i := 0; i < oc-cc; i++ {
			repaired += p.closeTag
		}
	}
	return repaired
}

// ExtractToolCallsFromText 从模型输出的文本/XML 中解析工具调用（导出供
// agent 对非 DeepSeekBackend 后端做文本兜底解析——modelgateway handle 等
// 包装类型不暴露 StreamEx，走普通 Stream 时结构化 tool_calls 不转发，
// 只能从文本收集后解析）。
func ExtractToolCallsFromText(content string) []types.ToolCall {
	if content == "" {
		return nil
	}
	calls := extractToolCallsInner(content)
	// 容错：原文解析后仍存在未闭合的工具调用标签（模型被截断）时，
	// 补全闭合标签重新解析并合并去重——保证"部分完整 + 部分截断"的
	// 多调用块全部被解析出来，而不是只取完整的那部分。
	if hasUnclosedToolCall(content) {
		if repaired := repairTruncatedToolCall(content); repaired != content {
			seen := make(map[string]bool, len(calls))
			for _, c := range calls {
				seen[c.ID+c.Function.Name+c.Function.Arguments] = true
			}
			for _, c := range extractToolCallsInner(repaired) {
				key := c.ID + c.Function.Name + c.Function.Arguments
				if !seen[key] {
					seen[key] = true
					calls = append(calls, c)
				}
			}
		}
	}
	return calls
}

func extractToolCallsInner(content string) []types.ToolCall {
	var calls []types.ToolCall
	seen := make(map[string]bool)
	add := func(id, name, args string) {
		if !knownToolNames[name] {
			return // 不在受信任工具表内，跳过避免误解析
		}
		// 文本兜底解析（XML/DSML/行/围栏格式）的调用没有 API 级 tool_call_id：
		// "text-*" 只是格式标记——多个调用时 ID 重复且非 OpenAI/DeepSeek 兼容
		// API 接受的 id 格式。若不重生成，工具执行后第二轮请求携带非法/重复
		// tool_call_id → API 400 → 模型永远拿不到工具结果 → 对话突然中止。
		// 这里为这类调用生成合法且批次内唯一的 ID：call_<name>_<序号>。
		if id == "" || strings.HasPrefix(id, "text-") {
			id = fmt.Sprintf("call_%s_%d", name, len(calls))
		}
		if seen[id+name+args] {
			return
		}
		seen[id+name+args] = true
		args = strings.TrimSpace(args)
		calls = append(calls, types.ToolCall{
			ID:   id,
			Type: "function",
			Function: types.FunctionCall{
				Name:      name,
				Arguments: args,
			},
		})
	}

	// 格式 1：围栏 tool_call 块（最高优先级，内容可能跨行 JSON）
	for _, m := range toolCallFenceRe.FindAllStringSubmatch(content, -1) {
		add("text-fence", m[1], m[2])
	}

	// 格式 2：XML <invoke name=...>（参数可能为 <parameter> 子元素 / JSON / key=value）
	for _, m := range toolCallXMLRe.FindAllStringSubmatch(content, -1) {
		args := m[2]
		if j, ok := xmlParamsToJSON(args); ok {
			args = j
		}
		add("text-xml", m[1], args)
	}

	// 格式 2b：DSML 竖线风格 <|invoke name=...|>（与 XML 等价但带竖线分隔符）
	// 参数使用 DSML 版 <parameter> 正则提取；未命中时原样（兼容 JSON / key=value）。
	for _, m := range dsmlInvokeRe.FindAllStringSubmatch(content, -1) {
		args := m[2]
		ms := dsmlParamRe.FindAllStringSubmatch(args, -1)
		if len(ms) > 0 {
			pm := make(map[string]any, len(ms))
			for _, sm := range ms {
				pm[sm[1]] = strings.TrimSpace(sm[2])
			}
			if b, err := json.Marshal(pm); err == nil {
				args = string(b)
			}
		}
		add("text-dsml", m[1], args)
	}

	// 格式 3：[tool_call_xxx] name: args 行
	for _, m := range toolCallLineRe.FindAllStringSubmatch(content, -1) {
		add("text-line", m[1], m[2])
	}

	// 格式 4：独立行 "name: args"（仅当该行不以 [tool_call 开头、且前面没有内容）
	// 防止把正文普通语句误解析；仅识别已知工具名 + 参数非空。
	// 先剔除已解析的围栏块/XML 块内容，避免同一处调用被二次匹配。
	remaining := toolCallFenceRe.ReplaceAllString(content, "")
	remaining = toolCallXMLRe.ReplaceAllString(remaining, "")
	remaining = dsmlInvokeRe.ReplaceAllString(remaining, "")
	lines := strings.Split(remaining, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[tool_call") || strings.HasPrefix(line, "```") {
			continue
		}
		for name := range knownToolNames {
			prefix := name + ":"
			if strings.HasPrefix(line, prefix) || strings.HasPrefix(line, name+"：") {
				args := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, prefix), name+"："))
				if args != "" && !strings.HasPrefix(args, "http") {
					add("text-line", name, args)
				}
				break
			}
		}
	}

	return calls
}

// stripTextToolBlocks 从正文中剔除已解析的文本工具调用块（XML/DSML/围栏/
// [tool_call] 行）与外壳标记（<tool_calls>/</tool_calls> 等壳不是用户内容），
// 保留其余正文原样输出。与 extractToolCallsFromText 使用相同正则，保证剔除的
// 是"同一批"调用块，不误删普通内容。
func stripTextToolBlocks(content string) string {
	if content == "" {
		return ""
	}
	// 截断的调用块（缺闭合标签）无法被下方完整闭合正则匹配 → 残留泄漏到正文。
	// 先按解析层同规则补全闭合，使截断块也能被剔除，保证"剔除的与解析的是同一批"。
	if hasUnclosedToolCall(content) {
		content = repairTruncatedToolCall(content)
	}
	remaining := toolCallFenceRe.ReplaceAllString(content, "")
	remaining = toolCallXMLRe.ReplaceAllString(remaining, "")
	remaining = dsmlInvokeRe.ReplaceAllString(remaining, "")
	// [tool_call_xxx] name: args 单行
	remaining = toolCallLineRe.ReplaceAllString(remaining, "")
	// 外壳标记（调用壳不是用户内容，剔除；正文中提到这些标记的概率极低且
	// 此函数仅在真调用块上下文中调用，不会误删正常正文）
	for _, shell := range []string{
		"<|DSML|tool_calls>", "</|DSML|tool_calls>", "<|DSML|/tool_calls>",
		"<|tool_calls|>", "<|/tool_calls|>", "</|tool_calls|>", "|>",
		"<tool_calls>", "</tool_calls>",
	} {
		remaining = strings.ReplaceAll(remaining, shell, "")
	}
	// 独立行 "name: args"（与 extractToolCallsFromText 格式 4 同规则）
	lines := strings.Split(remaining, "\n")
	var kept []string
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		matched := false
		for name := range knownToolNames {
			prefix := name + ":"
			if strings.HasPrefix(l, prefix) || strings.HasPrefix(l, name+"：") {
				matched = true
				break
			}
		}
		if !matched {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func extractContent(result map[string]any) string {
	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		return ""
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return ""
	}
	// Try "message" first (non-streaming response), then "delta" (streaming chunk)
	msg, ok := choice["message"].(map[string]any)
	if !ok {
		msg, ok = choice["delta"].(map[string]any)
	}
	if !ok {
		return ""
	}
	// reasoning_content = 思考过程（reasoning）
	// content           = 正文回答（answer）
	// 用  thinking 标签包裹思考，拼接正文，TUI 的 splitThinkTags 会正确分离
	var reasoning, content string
	if rc, ok := msg["reasoning_content"].(string); ok {
		reasoning = rc
	}
	if c, ok := msg["content"].(string); ok {
		content = c
	}
	if reasoning != "" && content != "" {
		return "<|reasoning|>" + reasoning + "<|/reasoning|>" + content
	}
	if reasoning != "" {
		return "<|reasoning|>" + reasoning + "<|/reasoning|>"
	}
	return content
}

func (d *DeepSeekBackend) GenerateWithExpertHint(ctx context.Context, messages []types.Message, hint ExpertHint) (string, error) {
	_ = hint
	resp, _, err := d.Chat(ctx, messages, nil)
	return resp, err
}

func (d *DeepSeekBackend) String() string {
	return fmt.Sprintf("DeepSeekBackend(model=%s, endpoint=%s)", d.currentModel(), d.endpoint)
}
