// Package types 定义 ContextLoom Agent 的共享数据类型。
// 按职责拆分为多个文件：
//
//	types.go   — 基础别名、消息、上下文块、会话、监工报告
//	model.go   — 模型 IO 类型（工具调用、Chat 请求/响应、流式块）
//	task.go    — 任务、Agent 状态、指令常量
//	acp.go     — ACP WebSocket 协议消息类型
//	errors.go  — 领域错误类型 + Result 泛型
package types

import (
	"encoding/json"
	"strings"
	"time"
)

// ── 基础类型别名 ──

// Score 是 ContextLoom 内部的统一评分类型（0-100）。
type Score float64

// ── 消息类型（OpenAI / DeepSeek 兼容） ──

// Message 是消息，兼容 OpenAI Chat Completions API。
//
// 字段语义：
//   - Role:    "system" | "user" | "assistant" | "tool"
//   - Content: 文本内容（string）或多模态内容（[]ContentPart）
//   - ReasoningContent: assistant 消息的思维链（deepseek thinking 模式必须
//     原样回传，否则 API 400 "reasoning_content ... must be passed back"）。
//     展示层实时展示、不写正文（正文在 Content）；但历史保存与回传必须保留。
//   - Name:    可选的发送者名称（用于区分同名 role 的不同参与者）
//   - ToolCallID: tool role 消息必须填写，关联到对应的 ToolCall.ID
//   - ToolCalls: assistant 消息可选，列出模型请求的工具调用
type Message struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"` // string 或 []ContentPart（多模态）
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Name             string     `json:"name,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ContentPart 是多模态消息的一个内容片段。
// Type 为 "text" 时使用 Text；为 "image_url" 时使用 ImageURL。
type ContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`
}

// ImageURLPart 是图片 URL 的内容片段。
type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "low" | "high" | "auto"
}

// ImageOmittedPlaceholder 是上下文压缩时图片片段被降级为的文本占位。
// 多模态消息中的 image_url 可达 MB 级，预算压缩必须优先砍图（防 API 400），
// 替换为占位文本后模型与用户都能知道原位置曾有图片。
const ImageOmittedPlaceholder = "[图片已省略]"

// ImageStrippedPlaceholder 是目标模型不支持多模态（如 DeepSeek 官方纯文本
// 模型，服务端 content 只接受 text 变体）时，image_url 片段被剥离为的文本占位。
// 与 ImageOmittedPlaceholder 区分：前者因预算压缩省略，后者因模型能力不足剥离。
const ImageStrippedPlaceholder = "[图片已省略：当前模型不支持视觉输入]"

// ── Message 辅助方法 ──

// ContentPartsOf 把消息 Content 归一化为多模态内容片段列表。
// 支持两种存储形态：
//   - []ContentPart：内存中的原生形态，返回副本（避免调用方改坏历史）；
//   - []any：JSON 反序列化产物（检查点恢复 / 事件日志加载后 Content 数组
//     会被解码为 []any），经 marshal/unmarshal 归一为强类型。
//
// string / nil / 其他类型返回 (nil, false)。
func ContentPartsOf(content any) ([]ContentPart, bool) {
	switch c := content.(type) {
	case []ContentPart:
		out := make([]ContentPart, len(c))
		copy(out, c)
		return out, true
	case []any:
		b, err := json.Marshal(c)
		if err != nil {
			return nil, false
		}
		var parts []ContentPart
		if err := json.Unmarshal(b, &parts); err != nil {
			return nil, false
		}
		return parts, true
	default:
		return nil, false
	}
}

// TextContent 返回消息的纯文本内容。
// 如果 Content 是 string，直接返回；如果是多模态片段列表（[]ContentPart 或
// JSON 反序列化的 []any），拼接所有 text 部分；其他类型返回空字符串。
func (m Message) TextContent() string {
	switch c := m.Content.(type) {
	case string:
		return c
	default:
		parts, ok := ContentPartsOf(m.Content)
		if !ok {
			return ""
		}
		var buf strings.Builder
		for _, part := range parts {
			if part.Type == "text" {
				buf.WriteString(part.Text)
			}
		}
		return buf.String()
	}
}

// ImagePartCount 返回消息内容中的图片片段数量（兼容 []ContentPart 与
// JSON 反序列化的 []any 两种形态）。用于识图检测、渲染与 token 估算。
func (m Message) ImagePartCount() int {
	parts, ok := ContentPartsOf(m.Content)
	if !ok {
		return 0
	}
	n := 0
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL != nil && part.ImageURL.URL != "" {
			n++
		}
	}
	return n
}

// ImageURLs 返回消息中全部 image_url 片段的 URL（data URI 或 http(s) 地址）。
// 用途：GUI 展示层渲染对话记录图片预览（后端持有图，JSON 输出层不能只给
// "[图片×N]" 计数文本——前端无图可渲染，对话记录看不到图片）。
// 兼容 []ContentPart 与 JSON 反序列化的 []any 两种形态；非多模态返回 nil。
func (m Message) ImageURLs() []string {
	parts, ok := ContentPartsOf(m.Content)
	if !ok {
		return nil
	}
	var urls []string
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL != nil && part.ImageURL.URL != "" {
			urls = append(urls, part.ImageURL.URL)
		}
	}
	return urls
}

// WithoutImages 返回剥离全部 image_url 片段后的消息（不修改原消息）。
//
// 用途：目标模型不支持多模态时（如 DeepSeek 官方纯文本模型，content 只接受
// text 变体），发送前必须剥离 image_url，否则 API 返回 400
// （unknown variant 'image_url', expected 'text'）。
//
// 降级规则：
//   - Content 为纯文本 / nil / 其他类型 → 原样返回，changed=false；
//   - 多模态片段列表 → 保留全部 text 部分，每张被剥离的图片追加一行
//     ImageStrippedPlaceholder 占位文本，changed=true；
//   - 片段列表仅含图片（无 text）→ Content 变为纯字符串占位文本。
func (m Message) WithoutImages() (Message, bool) {
	parts, ok := ContentPartsOf(m.Content)
	if !ok {
		return m, false
	}
	var texts []string
	imgCount := 0
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		} else if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL != "" {
			imgCount++
		}
	}
	if imgCount == 0 {
		return m, false
	}
	for i := 0; i < imgCount; i++ {
		texts = append(texts, ImageStrippedPlaceholder)
	}
	out := m
	out.Content = strings.Join(texts, "\n")
	return out, true
}

// SetTextContent 设置消息的纯文本内容。
func (m *Message) SetTextContent(text string) {
	m.Content = text
}

// NewTextMessage 创建一个纯文本消息的便捷构造器。
func NewTextMessage(role, content string) Message {
	return Message{Role: role, Content: content}
}

// ── ToolCall 辅助方法 ──

// FuncName 返回工具调用的函数名。
func (tc ToolCall) FuncName() string {
	return tc.Function.Name
}

// FuncArgs 返回工具调用的函数参数（JSON 字符串）。
func (tc ToolCall) FuncArgs() string {
	return tc.Function.Arguments
}

// NewToolCall 创建一个标准函数工具调用的便捷构造器。
func NewToolCall(id, name, argsJSON string) ToolCall {
	return ToolCall{
		ID:   id,
		Type: "function",
		Function: FunctionCall{
			Name:      name,
			Arguments: argsJSON,
		},
	}
}

// ── 上下文块与窗口 ──

// ContextBlock 是 Harness 引擎管理的最小上下文单元。
//
// Score 由 Harness.RelevanceScorer 计算：关键词重叠 85% + 时效性 15%。
// Dirty 标记表示内容已过期，需要重新索引。
type ContextBlock struct {
	ID       string    `json:"id"`
	Content  string    `json:"content"`
	Score    Score     `json:"score"`
	LastUsed time.Time `json:"last_used"`
	Dirty    bool      `json:"dirty"`
	// SessionID 归属会话（UUID）。空="项目级共享块"（代码/文档上下文，所有会话可见）；
	// 非空="会话私有块"（如被压缩的对话原文），仅该会话可检索到——防止同项目
	// 不同会话通过 harness_search 互相检索到对方对话内容导致任务串扰。
	SessionID string `json:"session_id,omitempty"`
}

// WindowSlot 是滑动窗口中的一个区域（anchor / core / ref）。
type WindowSlot struct {
	Region string         `json:"region"`
	Blocks []ContextBlock `json:"blocks"`
}

// ── 会话与事件 ──

// Session 表示一个对话会话的元数据。
// 完整对话内容通过 SessionEvent 日志流存储。
type Session struct {
	UUID         string    `json:"uuid"`
	Title        string    `json:"title"`
	Preview      string    `json:"preview,omitempty"` // 首条用户消息摘要，供列表预览对话内容
	Project      string    `json:"project"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count,omitempty"`
	TokenUsage   *Usage    `json:"token_usage,omitempty"`
	// CompactedMessages 是压缩后的对话历史快照（ctxmgr 五级渐进压缩后保留的消息）。
	// 压缩时持久化，重新加载会话时优先从快照恢复（跳过 JSONL 全量重放+重压缩），
	// 使 ctx 占比在重启/重新加载后立即等于压缩后的状态，不再回退到未压缩的完整用量。
	// 被压缩丢弃的原文完整保留在 JSONL 事件日志中，可通过 harness_search 检索找回。
	CompactedMessages []Message `json:"compacted_messages,omitempty"`
	// CompactedSummary 是压缩产生的滚动摘要文本（[历史摘要]），持久化后重新加载时
	// 直接注入，避免需要从 JSONL 重新生成。
	CompactedSummary string `json:"compacted_summary,omitempty"`
	// CompactedSeq 是快照写入时事件日志已覆盖到的最后一条事件 Seq。
	// 重新加载会话时以快照为压缩基线，把 Seq 大于该值的增量事件追加合并，
	// 修复"快照写入后的新对话在重连/切换会话加载时丢失"（GUI 中断断开后
	// 再继续对话不再连续的历史缺陷根因）。
	CompactedSeq int64 `json:"compacted_seq,omitempty"`
}

// SessionEvent 是会话事件日志中的一条记录。
type SessionEvent struct {
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // "user_msg" | "assistant_msg" | "tool_call" | "tool_result" | "error" | "status"
	Data      any       `json:"data"`
}

// ── 监工报告 ──

// OverseerReport 是监工对子 Agent 执行结果的四维评估报告。
type OverseerReport struct {
	NodeName     string  `json:"node_name"`
	Score        Score   `json:"score"`
	Passed       bool    `json:"passed"`
	Completeness float64 `json:"completeness"`   // 功能完整性 (40%)
	TestCoverage float64 `json:"test_coverage"`  // 测试覆盖 (30%)
	CodeQuality  float64 `json:"code_quality"`   // 代码质量 (20%)
	NoSideEffect float64 `json:"no_side_effect"` // 无副作用 (10%)
	Summary      string  `json:"summary"`
	// 防自我欺骗：子 Agent 声称的结果 vs 沙箱真实执行结果
	ClaimedResult *ToolResult `json:"claimed_result,omitempty"`
	ActualResult  *ToolResult `json:"actual_result,omitempty"`
}

// OverseerCheckpoint 是监工在节点树中记录的审批节点。
type OverseerCheckpoint struct {
	NodeName     string `json:"node_name"`
	ChildAgentID string `json:"child_agent_id"`
	Approved     bool   `json:"approved"`
	Reason       string `json:"reason,omitempty"`
}

// ── Skill 技能 ──

// Skill 技能定义（从 .contextloom/skills/<name>/SKILL.md 加载）
type Skill struct {
	Name        string `json:"name"`                // 技能名（目录名）
	Description string `json:"description"`         // 触发描述（frontmatter）
	Content     string `json:"content"`             // SKILL.md 正文（Markdown，注入系统提示词）
	Toolchain   string `json:"toolchain,omitempty"` // 关联工具链名（frontmatter toolchain:，§4.3.2 技能强制执行链；空=无）
	Dir         string `json:"dir"`                 // 技能目录绝对路径
}
