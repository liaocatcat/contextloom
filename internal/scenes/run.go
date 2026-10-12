package scenes

import (
	"context"
	"fmt"
	"time"
)

// 一键执行 Run 的结果状态常量（RunResult.Status）。
const (
	// StatusOK 执行成功（Runner 正常返回）。
	StatusOK = "ok"
	// StatusFailed 执行失败（Runner 返回错误）。
	StatusFailed = "failed"
	// StatusCanceled 执行被取消（ctx 提前取消）。
	StatusCanceled = "canceled"
)

// Runner 一键执行动作（注入式）。由调用方提供实现——负责按 inst.Scene 的装配信息
// 真正拉起员工/模型/工具链并返回产出文本。实现必须尊重 ctx：取消后应尽快返回。
type Runner func(ctx context.Context, inst Instance) (output string, err error)

// RunResult 一键执行的产出快照。
type RunResult struct {
	Output    string        // Runner 返回的产出文本（取消场景为空）
	Scene     Scene         // 场景快照
	StartedAt time.Time     // 开始时间
	Duration  time.Duration // 实际耗时
	Status    string        // StatusOK / StatusFailed / StatusCanceled
}

// Run 一键执行场景实例。
//
//   - runner 为 nil 直接报错；
//   - ctx 已取消或执行中取消：返回可读错误且 Status=StatusCanceled，绝不挂起调用方；
//   - runner 返回错误：Status=StatusFailed，错误向上包裹（保留原始错误链）。
func Run(ctx context.Context, inst Instance, runner Runner) (*RunResult, error) {
	if runner == nil {
		return nil, fmt.Errorf("scenes: Runner 不能为 nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	started := time.Now()

	// 执行前快速短路：ctx 已取消则直接返回 canceled。
	if err := ctx.Err(); err != nil {
		return &RunResult{Scene: inst.Scene, StartedAt: started, Duration: 0, Status: StatusCanceled},
			fmt.Errorf("scenes: 场景 %q 执行已取消：%w", inst.Scene.Name, err)
	}

	type outcome struct {
		output string
		err    error
	}
	// 带缓冲通道：取消后 Runner 的写入不阻塞，避免 goroutine 泄漏。
	done := make(chan outcome, 1)
	go func() {
		out, err := runner(ctx, inst)
		done <- outcome{output: out, err: err}
	}()

	select {
	case o := <-done:
		res := &RunResult{Output: o.output, Scene: inst.Scene, StartedAt: started, Duration: time.Since(started)}
		if o.err != nil {
			res.Status = StatusFailed
			return res, fmt.Errorf("scenes: 场景 %q 执行失败：%w", inst.Scene.Name, o.err)
		}
		res.Status = StatusOK
		return res, nil
	case <-ctx.Done():
		res := &RunResult{Scene: inst.Scene, StartedAt: started, Duration: time.Since(started), Status: StatusCanceled}
		return res, fmt.Errorf("scenes: 场景 %q 执行已取消：%w", inst.Scene.Name, ctx.Err())
	}
}
