package modelgateway

import (
	"context"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// handle 是 Select 返回的 per-agent 后端句柄：绑定一个池（而非单个实例），
// 每次 Chat/Stream 在池内轮询挑选健康实例；代理前获取池信号量（限流）、
// 失败走指数退避重试（重试换实例）、可重试失败计入实例熔断计数。
//
// 直通配置（temperature/top_p/effort）缓存在句柄上，每次挑中实例后应用，
// 保证同一 Agent 跨实例轮换时参数一致。
type handle struct {
	gw         *Gateway
	pool       *poolImpl
	targetName string // 路由目标池名（观测用）
	degraded   bool   // 是否发生降级（上层据此打标提示）
	notice     string // 降级原因文案

	setMu  sync.Mutex
	temp   *float64
	topP   *float64
	effort *string
	// streamTools 流式请求 tools 参数（透传底层实例；nil=不发送）
	streamTools []types.ToolDefinition
}

// 编译期断言：handle 实现完整 model.Backend 接口。
var _ model.Backend = (*handle)(nil)

// Degraded 报告句柄是否因熔断降级产生（true 时输出应带"已降级"提示）。
func (h *handle) Degraded() bool { return h.degraded }

// Notice 返回降级原因文案（未降级为空串）。
func (h *handle) Notice() string { return h.notice }

// PoolName 返回句柄当前绑定池名（观测用）。
func (h *handle) PoolName() string { return h.pool.name }

// TargetName 返回路由目标池名（路由观测用；降级时与 PoolName 不同）。
func (h *handle) TargetName() string { return h.targetName }

// ── 后端直通配置（缓存在句柄上，挑中实例后应用）──

func (h *handle) SetReasoningEffort(level string) {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	v := level
	h.effort = &v
}

// SetStreamTools 透传流式 tools 参数：缓存在句柄上，挑中实例后由
// applyParams 应用（与 temp/topP/effort 一致）。nil=禁用工具。
func (h *handle) SetStreamTools(tools []types.ToolDefinition) {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	h.streamTools = tools
}

func (h *handle) ReasoningEffort() string {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	if h.effort != nil {
		return *h.effort
	}
	return h.probe().inner.ReasoningEffort()
}

func (h *handle) SetTopP(p float64) {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	v := p
	h.topP = &v
}

func (h *handle) TopP() float64 {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	if h.topP != nil {
		return *h.topP
	}
	return h.probe().inner.TopP()
}

func (h *handle) SetTemperature(t float64) {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	v := t
	h.temp = &v
}

func (h *handle) Temperature() float64 {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	if h.temp != nil {
		return *h.temp
	}
	return h.probe().inner.Temperature()
}

// applyParams 把缓存的参数应用到实例（无缓存则保持实例默认）。
func (h *handle) applyParams(ins *poolInstance) {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	if h.temp != nil {
		ins.inner.SetTemperature(*h.temp)
	}
	if h.topP != nil {
		ins.inner.SetTopP(*h.topP)
	}
	if h.effort != nil {
		ins.inner.SetReasoningEffort(*h.effort)
	}
	// 流式 tools 透传：底层实例支持 StreamExBackend 才可设置（DeepSeekBackend）
	if h.streamTools != nil {
		if se, ok := ins.inner.(model.StreamExBackend); ok {
			se.SetStreamTools(h.streamTools)
		}
	}
}

// probe 返回一个健康实例用于直通查询；池不可用时返回兜底后端或 nil 语义值。
func (h *handle) probe() *poolInstance {
	if ins := h.pool.pickHealthy(time.Now()); ins != nil {
		return ins
	}
	if h.gw.fallback != nil {
		if ins := h.gw.fallback.pickHealthy(time.Now()); ins != nil {
			return ins
		}
	}
	return &poolInstance{inner: nilBackend{}}
}

func (h *handle) ModelID() string { return h.probe().inner.ModelID() }
func (h *handle) MaxContext() int { return h.probe().inner.MaxContext() }
func (h *handle) SupportsVision() bool {
	return h.probe().inner.SupportsVision()
}

// ── 信号量 ──

// acquire 获取池信号量（限流）；ctx 取消或等待超时返回错误。
func (h *handle) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timeout := h.pool.acquireTimeout
	if timeout <= 0 {
		select {
		case h.pool.sem <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case h.pool.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errSemTimeout
	}
}

func (h *handle) release() { <-h.pool.sem }

// ── Chat：轮询选实例 + 限流 + 重试（换实例）+ 熔断计数 ──

func (h *handle) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := h.acquire(ctx); err != nil {
		return "", nil, err
	}
	defer h.release()

	var lastErr error
	for attempt := 0; attempt < h.pool.retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		ins := h.pool.pickHealthy(time.Now())
		if ins == nil {
			return "", nil, ErrPoolTripped
		}
		h.applyParams(ins)
		text, calls, err := ins.inner.Chat(ctx, messages, tools)
		if err == nil {
			ins.recordSuccess()
			return text, calls, nil
		}
		if !retryable(err) {
			// 参数类等不可重试错误：不触发熔断计数，直接上抛
			return "", nil, err
		}
		ins.recordFailure(time.Now())
		lastErr = err
		if attempt < h.pool.retries-1 {
			if !sleepCtx(ctx, h.pool.backoff(attempt)) {
				return "", nil, ctx.Err()
			}
		}
	}
	return "", nil, lastErr
}

// ── Stream：转发流 + 限流 + 失败计数（流不重试，避免内容重复输出）──

func (h *handle) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := h.acquire(ctx); err != nil {
		out := make(chan string)
		errC := make(chan error, 1)
		close(out)
		errC <- err
		close(errC)
		return out, errC
	}
	ins := h.pool.pickHealthy(time.Now())
	if ins == nil {
		h.release()
		out := make(chan string)
		errC := make(chan error, 1)
		close(out)
		errC <- ErrPoolTripped
		close(errC)
		return out, errC
	}
	h.applyParams(ins)
	in, inErr := ins.inner.Stream(ctx, messages)
	return h.relay(ctx, ins, in, inErr)
}

// relay 转发流：消费内部两个 channel 转发到新 channel，全部关闭后释放
// 信号量（sync.Once 保证只释放一次）；errCh 收到错误则计入实例失败计数。
func (h *handle) relay(ctx context.Context, ins *poolInstance, in <-chan string, inErr <-chan error) (<-chan string, <-chan error) {
	out := make(chan string, 16)
	errOut := make(chan error, 1)
	go func() {
		var once sync.Once
		release := func() { once.Do(h.release) }
		defer release()
		defer close(out)
		defer close(errOut)
		failed := false
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-inErr:
				if !ok {
					inErr = nil
				} else {
					failed = true
					select {
					case errOut <- e:
					case <-ctx.Done():
						return
					}
				}
			case s, ok := <-in:
				if !ok {
					in = nil
				} else {
					select {
					case out <- s:
					case <-ctx.Done():
						return
					}
				}
			}
			if in == nil && inErr == nil {
				break
			}
		}
		if failed {
			ins.recordFailure(time.Now())
		} else {
			ins.recordSuccess()
		}
	}()
	return out, errOut
}

// sleepCtx 可中断睡眠：ctx 提前取消返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nilBackend 兜底直通后端：池全灭时 probe 的查询返回零值语义，不 panic。
type nilBackend struct{}

func (nilBackend) Chat(context.Context, []types.Message, []types.ToolDefinition) (string, []types.ToolCall, error) {
	return "", nil, ErrPoolTripped
}
func (nilBackend) Stream(context.Context, []types.Message) (<-chan string, <-chan error) {
	out := make(chan string)
	errC := make(chan error, 1)
	close(out)
	errC <- ErrPoolTripped
	close(errC)
	return out, errC
}
func (nilBackend) ModelID() string           { return "" }
func (nilBackend) MaxContext() int           { return 0 }
func (nilBackend) SupportsVision() bool      { return false }
func (nilBackend) SetReasoningEffort(string) {}
func (nilBackend) ReasoningEffort() string   { return "" }
func (nilBackend) SetTopP(float64)           {}
func (nilBackend) TopP() float64             { return 0 }
func (nilBackend) SetTemperature(float64)    {}
func (nilBackend) Temperature() float64      { return 0 }

// ── StreamEx：结构化流式转发（Content + ToolCalls + FinishReason）──
// model.StreamExBackend 实现。agent 在 gateway 路由路径下拿到的是 handle
// 而非 *model.DeepSeekBackend——旧逻辑因类型断言失败走普通 Stream（只转发
// 文本，工具调用被底层吞掉）→ 工具从不执行、对话一轮中止。此方法让 handle
// 完整转发底层实例的 StreamEx chunk，工具调用可达 agent 执行层。

func (h *handle) StreamEx(ctx context.Context, messages []types.Message) (<-chan model.StreamChunk, <-chan error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := h.acquire(ctx); err != nil {
		out := make(chan model.StreamChunk)
		errC := make(chan error, 1)
		close(out)
		errC <- err
		close(errC)
		return out, errC
	}
	ins := h.pool.pickHealthy(time.Now())
	if ins == nil {
		h.release()
		out := make(chan model.StreamChunk)
		errC := make(chan error, 1)
		close(out)
		errC <- ErrPoolTripped
		close(errC)
		return out, errC
	}
	h.applyParams(ins)
	if se, ok := ins.inner.(model.StreamExBackend); ok {
		in, inErr := se.StreamEx(ctx, messages)
		return h.relayEx(ctx, ins, in, inErr)
	}
	// 回退：底层不支持 StreamEx → 普通 Stream 包装为 Content-only chunk
	in, inErr := ins.inner.Stream(ctx, messages)
	return h.relayExText(ctx, ins, in, inErr)
}

// relayEx 转发 StreamChunk 流（语义同 relay，chunk 类型不同）。
func (h *handle) relayEx(ctx context.Context, ins *poolInstance, in <-chan model.StreamChunk, inErr <-chan error) (<-chan model.StreamChunk, <-chan error) {
	out := make(chan model.StreamChunk, 16)
	errOut := make(chan error, 1)
	go func() {
		var once sync.Once
		release := func() { once.Do(h.release) }
		defer release()
		defer close(out)
		defer close(errOut)
		failed := false
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-inErr:
				if !ok {
					inErr = nil
				} else {
					failed = true
					select {
					case errOut <- e:
					case <-ctx.Done():
						return
					}
				}
			case s, ok := <-in:
				if !ok {
					in = nil
				} else {
					select {
					case out <- s:
					case <-ctx.Done():
						return
					}
				}
			}
			if in == nil && inErr == nil {
				break
			}
		}
		if failed {
			ins.recordFailure(time.Now())
		} else {
			ins.recordSuccess()
		}
	}()
	return out, errOut
}

// relayExText 把普通 Stream（文本）包装为 Content-only StreamChunk 流。
func (h *handle) relayExText(ctx context.Context, ins *poolInstance, in <-chan string, inErr <-chan error) (<-chan model.StreamChunk, <-chan error) {
	out := make(chan model.StreamChunk, 16)
	errOut := make(chan error, 1)
	go func() {
		var once sync.Once
		release := func() { once.Do(h.release) }
		defer release()
		defer close(out)
		defer close(errOut)
		failed := false
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-inErr:
				if !ok {
					inErr = nil
				} else {
					failed = true
					select {
					case errOut <- e:
					case <-ctx.Done():
						return
					}
				}
			case s, ok := <-in:
				if !ok {
					in = nil
				} else {
					select {
					case out <- model.StreamChunk{Content: s}:
					case <-ctx.Done():
						return
					}
				}
			}
			if in == nil && inErr == nil {
				break
			}
		}
		if failed {
			ins.recordFailure(time.Now())
		} else {
			ins.recordSuccess()
		}
	}()
	return out, errOut
}
