package modelgateway

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/contextloom/contextloom-agent/internal/model"
)

// poolInstance 池内单个后端实例：独立失败计数与熔断冷却（无锁）。
type poolInstance struct {
	inner model.Backend

	// failThreshold 连续失败熔断阈值（免熔断语义：<=1 恒健康）。
	failThreshold int64
	// cooldown 熔断冷却时长，到期自动恢复（半开：下一次成功即清零）。
	cooldown time.Duration

	failures     atomic.Int64 // 连续失败计数（成功清零）
	trippedUntil atomic.Int64 // 熔断截止时刻（unix nano），0=健康
}

func newPoolInstance(p *poolImpl, b model.Backend) *poolInstance {
	return &poolInstance{
		inner:         b,
		failThreshold: p.threshold,
		cooldown:      p.cooldown,
	}
}

// available 判断实例当前是否可用；冷却到期自动恢复为半开（不预清零计数，
// 由下一次成功清零——即标准"半开探测"语义）。
func (ins *poolInstance) available(now time.Time) bool {
	until := ins.trippedUntil.Load()
	if until == 0 {
		return true
	}
	return now.UnixNano() >= until
}

func (ins *poolInstance) tripped(now time.Time) bool { return !ins.available(now) }

// recordFailure 记录一次可重试失败：达到阈值则熔断（写入冷却截止时刻）。
func (ins *poolInstance) recordFailure(now time.Time) {
	if ins.failThreshold <= 1 {
		return // 免熔断
	}
	if ins.failures.Add(1) >= ins.failThreshold {
		ins.trippedUntil.Store(now.Add(ins.cooldown).UnixNano())
	}
}

// recordSuccess 成功清零失败计数并解除熔断标记。
func (ins *poolInstance) recordSuccess() {
	ins.failures.Store(0)
	ins.trippedUntil.Store(0)
}

// poolImpl 实例池运行时状态。instances 构造后只读，无需互斥；
// 信号量 sem 承担并发限流（容量 = max_concurrent）。
type poolImpl struct {
	name string
	cfg  PoolConfig

	instances []*poolInstance

	sem            chan struct{} // 信号量：每池并发上限
	acquireTimeout time.Duration // 信号量等待上限（<=0 表示跟随 ctx）
	retries        int           // 单次调用最大尝试次数（含首试）
	base           time.Duration // 重试退避基数
	factor         float64       // 重试退避指数因子
	threshold      int64         // 实例熔断阈值
	cooldown       time.Duration // 熔断冷却时长

	balance atomic.Uint64 // 轮询游标
}

// buildPool 按配置建池：优先用预注入 Instances（测试/程序化），
// 否则按 keys 调 model.NewBackend 建厂（每个 key 一个实例）。
// 实例数为 0（无 key 且无注入）的池为"空池"，Select 时视为不可用。
func buildPool(name string, cfg PoolConfig) *poolImpl {
	mc := cfg.MaxConcurrent
	if mc <= 0 {
		mc = 4
	}
	th := int64(cfg.FailThreshold)
	if th < 0 {
		th = 0
	}
	if th == 0 {
		th = 3
	}
	retries := cfg.Retries
	if retries <= 0 {
		retries = 3
	}
	base := cfg.RetryBase.Duration
	if base <= 0 {
		base = 100 * time.Millisecond
	}
	factor := cfg.RetryFactor
	if factor <= 0 {
		factor = 3
	}
	cooldown := cfg.Cooldown.Duration
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	timeout := cfg.AcquireTimeout.Duration
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	p := &poolImpl{
		name:           name,
		cfg:            cfg,
		sem:            make(chan struct{}, mc),
		acquireTimeout: timeout,
		retries:        retries,
		base:           base,
		factor:         factor,
		threshold:      th,
		cooldown:       cooldown,
	}
	if len(cfg.Instances) > 0 {
		for _, b := range cfg.Instances {
			applyVisionCapability(b, cfg.SupportsVision)
			p.instances = append(p.instances, newPoolInstance(p, b))
		}
		return p
	}
	keys := cfg.allKeys()
	for _, k := range keys {
		b, err := model.NewBackend(cfg.Backend, cfg.Endpoint, k, cfg.Model, cfg.MaxContext, cfg.SendEffort)
		if err != nil {
			continue
		}
		applyVisionCapability(b, cfg.SupportsVision)
		p.instances = append(p.instances, newPoolInstance(p, b))
	}
	return p
}

// visionCapable 多模态能力注入接口：DeepSeekBackend（含 OpenAI 兼容通道）
// 实现。池内实例建厂后统一按池配置注入 supports_vision，否则网关路由路径
// 下 SupportsVision() 恒为 false，会把支持视觉的模型误判为纯文本而降级。
type visionCapable interface{ SetSupportsVision(bool) }

// applyVisionCapability 把池配置的多模态能力标志注入实例。仅在声明为 true
// 时注入：false 时保持实例自身能力（default/兜底池由注入后端构造，其能力
// 已在 runtime.New 按激活预设配置好，盲注 false 会把它清掉）；实例不支持该
// 接口（如测试 mock）时静默跳过——与既有"未知后端按不支持处理"一致。
func applyVisionCapability(b model.Backend, supports bool) {
	if !supports {
		return
	}
	if vc, ok := b.(visionCapable); ok {
		vc.SetSupportsVision(true)
	}
}

// pickHealthy 轮询选择一个可用实例；全部熔断或空池返回 nil。
func (p *poolImpl) pickHealthy(now time.Time) *poolInstance {
	n := len(p.instances)
	if n == 0 {
		return nil
	}
	start := int(p.balance.Add(1)-1) % n
	for i := 0; i < n; i++ {
		ins := p.instances[(start+i)%n]
		if ins.available(now) {
			return ins
		}
	}
	return nil
}

// tripped 池整体熔断判定：空池或所有实例均不可用。
// 注意：使用不消耗轮询游标的扫描式检查（Select 的健康探测不应干扰分配公平性）。
func (p *poolImpl) tripped(now time.Time) bool {
	for _, ins := range p.instances {
		if ins.available(now) {
			return false
		}
	}
	return true
}

// healthyCount 可用实例数（观测用）。
func (p *poolImpl) healthyCount(now time.Time) int {
	c := 0
	for _, ins := range p.instances {
		if ins.available(now) {
			c++
		}
	}
	return c
}

// backoff 第 attempt 次重试（attempt 从 0 起）前的退避时长：base * factor^attempt。
func (p *poolImpl) backoff(attempt int) time.Duration {
	d := float64(p.base)
	for i := 0; i < attempt; i++ {
		d *= p.factor
	}
	return time.Duration(d)
}

// PoolStatus 池健康快照（供观测/UI/测试断言）。
type PoolStatus struct {
	Name      string // 池名
	Instances int    // 实例总数
	Healthy   int    // 当前可用实例数
	Tripped   bool   // 池整体是否熔断（不可用）
	InFlight  int    // 信号量当前占用（进行中的请求）
	Capacity  int    // 信号量容量（max_concurrent）
}

func (p *poolImpl) status(now time.Time) PoolStatus {
	return PoolStatus{
		Name:      p.name,
		Instances: len(p.instances),
		Healthy:   p.healthyCount(now),
		Tripped:   p.tripped(now),
		InFlight:  len(p.sem),
		Capacity:  cap(p.sem),
	}
}

// fmtStatus 供降级提示文案复用。
func (p *poolImpl) fmtStatus() string {
	return fmt.Sprintf("池 %q（%d/%d 实例可用）", p.name, p.healthyCount(time.Now()), len(p.instances))
}
