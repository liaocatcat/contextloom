package modelgateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/internal/model"
)

// Req 一次 Select 的路由请求上下文（对应方案 §4.2.2 的四类谓词）。
// 空字段不参与匹配（budget<=low 规则下，空 Budget 视为最低档）。
type Req struct {
	Role   string // 员工角色（coder/reviewer/...）
	Task   string // 任务类型（vision/chat/summary/...）
	Budget string // 预算档位（high/low）
	Scene  string // 场景名
}

// Gateway 模型网关（§4.2）：规则路由 → 实例池（轮询 + 信号量限流 +
// 退避重试 + 连续失败熔断）→ 整池降级到 default 池 → 最后兜底到注入后端。
// 默认池语义：未配置 gateway 或未命中任何规则时，行为与旧路径一致。
type Gateway struct {
	defaultName string
	rules       []RuleConfig
	pools       map[string]*poolImpl
	fallback    *poolImpl // 构造时注入的既有默认后端（单实例池，最后兜底）

	mu        sync.RWMutex
	onDegrade func(notice string)
}

// New 构造网关。fallback 可选：既有默认后端（如 runtime.Backend）——
// 所有池（含 default）均不可用时作为最后兜底，保证旧路径零改动、零学习成本。
func New(cfg GatewayConfig, fallback ...model.Backend) (*Gateway, error) {
	g := &Gateway{
		defaultName: strings.TrimSpace(cfg.Default),
		rules:       cfg.Rules,
		pools:       make(map[string]*poolImpl, len(cfg.Pools)+1),
	}
	if g.defaultName == "" {
		g.defaultName = "default"
	}
	for name, pc := range cfg.Pools {
		g.pools[name] = buildPool(name, pc)
	}
	// default 池缺省时：若有注入后端 → 作为 default 池实例（旧路径零改动，
	// Select 未命中规则走 default 池 = 既有 Backend，无降级标记）；
	// 无注入后端 → 空池，运行时视为不可用，Select 报错。
	if _, ok := g.pools[g.defaultName]; !ok {
		if len(fallback) > 0 && fallback[0] != nil {
			g.pools[g.defaultName] = buildPool(g.defaultName, PoolConfig{Instances: []model.Backend{fallback[0]}})
		} else {
			g.pools[g.defaultName] = buildPool(g.defaultName, PoolConfig{})
		}
	}
	// 兜底后端单实例池：default 池熔断后的最后防线（独立失败计数）。
	for _, b := range fallback {
		if b != nil {
			g.fallback = buildPool("__fallback__", PoolConfig{Instances: []model.Backend{b}})
		}
	}
	return g, nil
}

// SetOnDegrade 注册降级事件回调（用户可见提示："已降级到默认池"）。
func (g *Gateway) SetOnDegrade(fn func(notice string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.onDegrade = fn
}

func (g *Gateway) fireDegrade(notice string) {
	g.mu.RLock()
	fn := g.onDegrade
	g.mu.RUnlock()
	if fn != nil {
		fn(notice)
	}
}

// Select 按路由规则选择 per-agent 后端句柄（§4.2.3）。规则自上而下匹配，
// 首个命中生效；未命中走 default 池；目标池熔断时自动降级（句柄带
// Degraded 标记，上层可在输出前打标提示用户）。
func (g *Gateway) Select(ctx context.Context, req Req) (model.Backend, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target := g.defaultName
	for _, r := range g.rules {
		if r.When.match(req) {
			target = r.Use
			break
		}
	}
	return g.selectPool(ctx, target)
}

// selectPool 目标池可用 → 健康句柄；不可用 → 按级降级
// （非 default 池 → default 池 → fallback 后端），每级都带降级提示。
func (g *Gateway) selectPool(ctx context.Context, target string) (model.Backend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	p := g.pools[target]
	if p != nil && !p.tripped(now) {
		return &handle{gw: g, pool: p, targetName: target}, nil
	}

	// ── 目标池不可用：构造降级提示并逐级降级 ──
	notice := fmt.Sprintf("模型池 %q 不可用（%s），已降级到默认池 %q",
		target, statusText(p), g.defaultName)
	if target == g.defaultName {
		notice = fmt.Sprintf("默认模型池不可用（%s），已降级到兜底后端", statusText(p))
	}
	g.fireDegrade(notice)

	if target != g.defaultName {
		if dp := g.pools[g.defaultName]; dp != nil && !dp.tripped(now) {
			return &handle{gw: g, pool: dp, targetName: target, degraded: true, notice: notice}, nil
		}
		// default 池也不可用：补充提示，继续走 fallback
		g.fireDegrade(fmt.Sprintf("默认模型池也不可用（%s），已降级到兜底后端", statusText(g.pools[g.defaultName])))
	}
	if g.fallback != nil && !g.fallback.tripped(now) {
		return &handle{gw: g, pool: g.fallback, targetName: target, degraded: true, notice: notice}, nil
	}
	return nil, fmt.Errorf("%w（路由目标 %q，默认池与兜底后端均不可用）", ErrPoolTripped, target)
}

// statusText 池状态文案；池不存在时给出占位描述（避免 nil 解引用）。
func statusText(p *poolImpl) string {
	if p == nil {
		return "池未配置（0 实例）"
	}
	return p.fmtStatus()
}

// PoolNames 返回全部池名（含自动建出的 default，排序稳定）。
func (g *Gateway) PoolNames() []string {
	out := make([]string, 0, len(g.pools))
	for name := range g.pools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// PoolStatus 返回指定池的健康快照（观测/GUI/测试用）；池不存在返回 ok=false。
func (g *Gateway) PoolStatus(name string) (PoolStatus, bool) {
	p, ok := g.pools[name]
	if !ok {
		return PoolStatus{}, false
	}
	return p.status(time.Now()), true
}
