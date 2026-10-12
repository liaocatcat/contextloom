package model

import (
	"context"
	"sync"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ChainBackend 多来源模型组合后端：把若干来源后端（可跨 provider/endpoint/
// model）按顺序串成一条降级链，实现"多来源模型的搭配组合"。
//
// 语义（零外部依赖，纯标准库）：
//   - Chat：顺序尝试来源，首个成功即返回；全部失败返回最后一个错误。
//   - Stream/StreamEx：在向调用方产出任何内容【之前】失败，自动切换到下一
//     来源；一旦已产出内容再失败，则不再切换（直接上抛错误），避免把两个
//     来源的半截输出拼接成串流/内容重复。
//   - 参数 setter（temperature/top_p/effort/tools）：广播到全部来源，保证
//     同一 Agent 跨来源切换时参数一致。
//
// 典型用法：员工档案声明主模型 + 备选来源（model.preset + model.fallbacks），
// 运行时把各预设解析为后端实例后 NewChainBackend 组合——主来源限流/熔断/
// 不可用时自动切到备选，无需依赖 gateway 规则。
type ChainBackend struct {
	sources []Backend

	mu     sync.Mutex
	temp   *float64
	topP   *float64
	effort *string
	tools  []types.ToolDefinition
}

// 编译期断言：组合后端同时满足基础与增强流式接口。
var (
	_ Backend         = (*ChainBackend)(nil)
	_ StreamExBackend = (*ChainBackend)(nil)
)

// NewChainBackend 构造组合后端。过滤 nil、去重保序（同一实例重复只留首次）。
// 有效来源为空时返回 nil（调用方据此回退单一后端，保持零破坏）。
func NewChainBackend(sources ...Backend) *ChainBackend {
	seen := make(map[Backend]struct{}, len(sources))
	var out []Backend
	for _, b := range sources {
		if b == nil {
			continue
		}
		if _, dup := seen[b]; dup {
			continue
		}
		seen[b] = struct{}{}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil
	}
	return &ChainBackend{sources: out}
}

// Sources 返回来源列表只读快照（观测/测试用）。
func (c *ChainBackend) Sources() []Backend {
	out := make([]Backend, len(c.sources))
	copy(out, c.sources)
	return out
}

// ModelID 返回主来源（第一来源）的模型 ID。
func (c *ChainBackend) ModelID() string {
	if len(c.sources) == 0 {
		return ""
	}
	return c.sources[0].ModelID()
}

// MaxContext 返回各来源上下文窗口的保守下界（取正数最小值）；全部未知时
// 回退主来源（不返回 0 以免误触发极端压缩）。
func (c *ChainBackend) MaxContext() int {
	min := 0
	for _, b := range c.sources {
		n := b.MaxContext()
		if n <= 0 {
			continue
		}
		if min == 0 || n < min {
			min = n
		}
	}
	if min == 0 {
		return c.sources[0].MaxContext()
	}
	return min
}

// SupportsVision 取来源并集：任一来源支持多模态即视为支持（组合链上只要
// 有一个视觉来源，识图任务就不会被误降级）。
func (c *ChainBackend) SupportsVision() bool {
	for _, b := range c.sources {
		if b.SupportsVision() {
			return true
		}
	}
	return false
}

// ── 参数 setter：缓存并广播到全部来源 ──

// SetReasoningEffort 广播 reasoning_effort 到全部来源。
func (c *ChainBackend) SetReasoningEffort(level string) {
	c.mu.Lock()
	v := level
	c.effort = &v
	srcs := c.sources
	c.mu.Unlock()
	for _, b := range srcs {
		b.SetReasoningEffort(level)
	}
}

// ReasoningEffort 返回缓存值（未设置时取主来源）。
func (c *ChainBackend) ReasoningEffort() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.effort != nil {
		return *c.effort
	}
	return c.sources[0].ReasoningEffort()
}

// SetTopP 广播 top_p 到全部来源。
func (c *ChainBackend) SetTopP(p float64) {
	c.mu.Lock()
	v := p
	c.topP = &v
	srcs := c.sources
	c.mu.Unlock()
	for _, b := range srcs {
		b.SetTopP(p)
	}
}

// TopP 返回缓存值（未设置时取主来源）。
func (c *ChainBackend) TopP() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.topP != nil {
		return *c.topP
	}
	return c.sources[0].TopP()
}

// SetTemperature 广播 temperature 到全部来源。
func (c *ChainBackend) SetTemperature(t float64) {
	c.mu.Lock()
	v := t
	c.temp = &v
	srcs := c.sources
	c.mu.Unlock()
	for _, b := range srcs {
		b.SetTemperature(t)
	}
}

// Temperature 返回缓存值（未设置时取主来源）。
func (c *ChainBackend) Temperature() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.temp != nil {
		return *c.temp
	}
	return c.sources[0].Temperature()
}

// SetStreamTools 广播流式 tools 到支持 StreamExBackend 的来源。
func (c *ChainBackend) SetStreamTools(tools []types.ToolDefinition) {
	c.mu.Lock()
	c.tools = tools
	srcs := c.sources
	c.mu.Unlock()
	for _, b := range srcs {
		if se, ok := b.(StreamExBackend); ok {
			se.SetStreamTools(tools)
		}
	}
}

// ── Chat：顺序尝试，首个成功即返回 ──

// Chat 顺序尝试各来源，首个成功返回；全部失败返回最后一个错误。
func (c *ChainBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for _, src := range c.sources {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		text, calls, err := src.Chat(ctx, messages, tools)
		if err == nil {
			return text, calls, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = context.Canceled
	}
	return "", nil, lastErr
}

// ── Stream：首块前失败自动切换来源 ──

// Stream 返回文本流。首个来源在产出任何内容前失败 → 切换到下一来源；
// 已产出内容再失败 → 直接上抛（不切换，避免拼接串流）。
func (c *ChainBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan string, 256)
	errOut := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errOut)
		var lastErr error
		for _, src := range c.sources {
			if err := ctx.Err(); err != nil {
				errOut <- err
				return
			}
			in, inErr := src.Stream(ctx, messages)
			forwarded, err := relayText(ctx, out, in, inErr)
			if err == nil {
				return // 该来源正常完成
			}
			if ctx.Err() != nil { // ctx 取消：终止整链
				errOut <- err
				return
			}
			lastErr = err
			if forwarded {
				errOut <- lastErr // 已产出内容：不切换，上抛
				return
			}
		}
		if lastErr != nil {
			errOut <- lastErr
		}
	}()
	return out, errOut
}

// ── StreamEx：结构化流式，首块前失败自动切换来源 ──

// StreamEx 返回结构化 chunk 流（Content + ToolCalls + FinishReason）。切换
// 语义同 Stream；不支持 StreamEx 的来源回退为 Content-only 包装。
func (c *ChainBackend) StreamEx(ctx context.Context, messages []types.Message) (<-chan StreamChunk, <-chan error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan StreamChunk, 256)
	errOut := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errOut)
		var lastErr error
		for _, src := range c.sources {
			if err := ctx.Err(); err != nil {
				errOut <- err
				return
			}
			var (
				in    <-chan StreamChunk
				inErr <-chan error
			)
			if se, ok := src.(StreamExBackend); ok {
				in, inErr = se.StreamEx(ctx, messages)
			} else {
				textCh, textErr := src.Stream(ctx, messages)
				in, inErr = wrapTextAsChunks(ctx, textCh, textErr)
			}
			forwarded, err := relayChunks(ctx, out, in, inErr)
			if err == nil {
				return
			}
			if ctx.Err() != nil {
				errOut <- err
				return
			}
			lastErr = err
			if forwarded {
				errOut <- lastErr
				return
			}
		}
		if lastErr != nil {
			errOut <- lastErr
		}
	}()
	return out, errOut
}

// relayText 消费单来源文本流并转发到 out，返回 (是否已转发内容, 错误)。
// err==nil 表示该来源正常完成；err!=nil 表示失败（ctx 取消或来源错误）。
func relayText(ctx context.Context, out chan<- string, in <-chan string, inErr <-chan error) (bool, error) {
	forwarded := false
	var lastErr error
	for in != nil || inErr != nil {
		select {
		case <-ctx.Done():
			return forwarded, ctx.Err()
		case e, ok := <-inErr:
			if !ok {
				inErr = nil
			} else if e != nil {
				lastErr = e
			}
		case s, ok := <-in:
			if !ok {
				in = nil
			} else {
				forwarded = true
				select {
				case out <- s:
				case <-ctx.Done():
					return forwarded, ctx.Err()
				}
			}
		}
	}
	return forwarded, lastErr
}

// relayChunks 语义同 relayText，chunk 类型不同。
func relayChunks(ctx context.Context, out chan<- StreamChunk, in <-chan StreamChunk, inErr <-chan error) (bool, error) {
	forwarded := false
	var lastErr error
	for in != nil || inErr != nil {
		select {
		case <-ctx.Done():
			return forwarded, ctx.Err()
		case e, ok := <-inErr:
			if !ok {
				inErr = nil
			} else if e != nil {
				lastErr = e
			}
		case s, ok := <-in:
			if !ok {
				in = nil
			} else {
				forwarded = true
				select {
				case out <- s:
				case <-ctx.Done():
					return forwarded, ctx.Err()
				}
			}
		}
	}
	return forwarded, lastErr
}

// wrapTextAsChunks 把纯文本流包装为 Content-only 的 StreamChunk 流。
func wrapTextAsChunks(ctx context.Context, in <-chan string, inErr <-chan error) (<-chan StreamChunk, <-chan error) {
	out := make(chan StreamChunk, 256)
	errOut := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errOut)
		for in != nil || inErr != nil {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-inErr:
				if !ok {
					inErr = nil
				} else if e != nil {
					select {
					case errOut <- e:
					case <-ctx.Done():
					}
					return
				}
			case s, ok := <-in:
				if !ok {
					in = nil
				} else {
					select {
					case out <- StreamChunk{Content: s}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out, errOut
}
