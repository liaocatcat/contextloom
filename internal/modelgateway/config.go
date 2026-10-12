// Package modelgateway 实现「工具链体系升级方案」§4.2 的模型网关：
// 多模型实例池化、规则路由、并发限流、熔断降级与指数退避重试。
// 全部基于标准库与 gopkg.in/yaml.v3，不引入任何新依赖；包内实例
// 复用 internal/model.NewBackend 工厂创建，天然跨平台。
package modelgateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/contextloom/contextloom-agent/internal/model"
	"gopkg.in/yaml.v3"
)

// Duration 支持 yaml 中字符串时长（如 "100ms"、"30s"）与整数纳秒两种写法。
type Duration struct {
	time.Duration
}

// UnmarshalYAML 先尝试字符串（time.ParseDuration），失败再尝试整数纳秒。
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err == nil {
		dur, err := time.ParseDuration(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("modelgateway: 非法时长 %q: %w", s, err)
		}
		d.Duration = dur
		return nil
	}
	var ns int64
	if err := node.Decode(&ns); err != nil {
		return fmt.Errorf("modelgateway: 时长需为字符串（如 100ms）或整数纳秒: %w", err)
	}
	d.Duration = time.Duration(ns)
	return nil
}

// MarshalYAML 以字符串形式输出时长（可读、可往返）。
func (d Duration) MarshalYAML() (any, error) {
	return d.Duration.String(), nil
}

// GatewayConfig 模型网关配置，对应方案 §4.2 的 gateway: 节。
type GatewayConfig struct {
	// Default 默认池名：未命中任何规则时的目标池，也是降级兜底池。空则取 "default"。
	Default string `yaml:"default"`
	// Rules 路由规则，自上而下匹配，首个命中生效；未命中走 default 池。
	Rules []RuleConfig `yaml:"rules"`
	// Pools 实例池定义，key 为池名（即路由目标）。
	Pools map[string]PoolConfig `yaml:"pools"`
}

// RuleConfig 一条路由规则：When 谓词命中 → 使用 Use 池。
type RuleConfig struct {
	When WhenConfig `yaml:"when"`
	Use  string     `yaml:"use"`
}

// WhenConfig 路由谓词。Role/Task/Budget/Scene 四类字段：空字段不参与匹配，
// 非空字段全部命中（交集）才算整条规则命中；MatchAll 表示通配恒命中。
type WhenConfig struct {
	Role   string `yaml:"role"`
	Task   string `yaml:"task"`
	Budget string `yaml:"budget"`
	Scene  string `yaml:"scene"`

	// MatchAll 通配：恒命中（when: "default" / "*" 的解析结果）。
	MatchAll bool `yaml:"-"`
	// budgetLE 记录 Budget 是否以 "<=" 声明（预算不高于规则档位即命中，
	// 空预算视为最低档；档位枚举为 high/low，等价于相等匹配的宽松形态）。
	budgetLE bool
}

// UnmarshalYAML 支持两种写法：
//
//	字符串：when: "role=coder" / "role=coder,task=vision" / "budget<=low" / "default" / "*"
//	映射：  when: { role: coder, task: vision }
func (w *WhenConfig) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return w.parseString(node.Value)
	case yaml.MappingNode:
		var aux struct {
			Role   string `yaml:"role"`
			Task   string `yaml:"task"`
			Budget string `yaml:"budget"`
			Scene  string `yaml:"scene"`
		}
		if err := node.Decode(&aux); err != nil {
			return err
		}
		w.Role, w.Task, w.Budget, w.Scene = aux.Role, aux.Task, aux.Budget, aux.Scene
		w.MatchAll = false
		return nil
	default:
		return fmt.Errorf("modelgateway: when 需为字符串或映射")
	}
}

// MarshalYAML 通配时输出 "default"，否则输出逗号分隔谓词字符串
// （与 UnmarshalYAML 的 parseString 完全对偶，确保 budget<=low 等
// 语义在 yaml 往返中不丢失）。
func (w WhenConfig) MarshalYAML() (any, error) {
	if w.MatchAll {
		return "default", nil
	}
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("role", w.Role)
	add("task", w.Task)
	if w.Budget != "" {
		op := "="
		if w.budgetLE {
			op = "<="
		}
		parts = append(parts, "budget"+op+w.Budget)
	}
	add("scene", w.Scene)
	if len(parts) == 0 {
		return "default", nil
	}
	return strings.Join(parts, ","), nil
}

func (w *WhenConfig) parseString(s string) error {
	s = strings.TrimSpace(s)
	if s == "" || s == "default" || s == "*" {
		w.MatchAll = true
		return nil
	}
	w.MatchAll = false
	// 分隔符支持 "," 与 "&"（叠加即交集）
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '&' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := "", "", false
		if a, b, found := strings.Cut(part, "<="); found {
			k, v, ok = a, b, true
			if strings.EqualFold(strings.TrimSpace(a), "budget") {
				w.budgetLE = true
			}
		} else if a, b, found := strings.Cut(part, "="); found {
			k, v, ok = a, b, true
		}
		if !ok {
			return fmt.Errorf("modelgateway: 非法 when 谓词 %q（应为 key=value、budget<=档位 或 default）", part)
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "role":
			w.Role = v
		case "task":
			w.Task = v
		case "budget":
			w.Budget = v
		case "scene":
			w.Scene = v
		default:
			return fmt.Errorf("modelgateway: 未知 when 键 %q（支持 role/task/budget/scene）", k)
		}
	}
	return nil
}

// ParseWhen 解析谓词字符串（程序化构造用，等价于 yaml 中的 when: 字符串写法）：
// "role=coder" / "role=coder,task=vision" / "budget<=low" / "default" / "*"。
func ParseWhen(s string) (WhenConfig, error) {
	var w WhenConfig
	if err := w.parseString(s); err != nil {
		return WhenConfig{}, err
	}
	return w, nil
}

// match 判断 req 是否命中该谓词：非空字段全部相等（交集），全空/通配恒真。
func (w WhenConfig) match(req Req) bool {
	if w.MatchAll {
		return true
	}
	if w.Role != "" && w.Role != req.Role {
		return false
	}
	if w.Task != "" && w.Task != req.Task {
		return false
	}
	if w.Budget != "" {
		if w.budgetLE {
			// "budget<=low"：请求未声明预算（视为最低档）或声明档位相符即命中。
			if req.Budget != "" && req.Budget != w.Budget {
				return false
			}
		} else if req.Budget != w.Budget {
			return false
		}
	}
	if w.Scene != "" && w.Scene != req.Scene {
		return false
	}
	return true
}

// PoolConfig 实例池定义：同名预设按 keys 数量创建多个 Backend 实例。
type PoolConfig struct {
	// Backend 后端类型名（deepseek/openai/openai-compatible/llama/ollama/local/mock），
	// 空则默认 openai-compatible，透传给 model.NewBackend。
	Backend  string `yaml:"backend"`
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"` // 模型 ID

	// APIKey/Key/Keys 三种写法取并集去重（多 Key 池化轮换）。
	APIKey string   `yaml:"api_key"`
	Key    string   `yaml:"key"`
	Keys   []string `yaml:"keys"`

	MaxContext int  `yaml:"max_context"`
	SendEffort bool `yaml:"send_effort"`

	// SupportsVision 多模态识图能力标志：与同名 models 预设桥接，池内实例
	// 建厂后统一注入。缺失此字段会让网关路由路径（配置了 gateway: 节时
	// 全部请求都走此路径）的 SupportsVision() 恒为 false——即便配置里
	// supports_vision=true，也会误判"当前模型不支持视觉输入"而降级。
	SupportsVision bool `yaml:"supports_vision"`

	// Instances 预注入的后端实例（yaml:"-"）：测试或程序化构造用。
	// 非空时不再按 keys 调工厂建厂，直接作为池内实例使用。
	Instances []model.Backend `yaml:"-"`

	// MaxConcurrent 每池信号量容量（并发上限），默认 4。
	MaxConcurrent int `yaml:"max_concurrent"`
	// FailThreshold 实例连续失败 N 次即熔断，默认 3。
	FailThreshold int `yaml:"fail_threshold"`
	// Retries 每次调用的最大尝试次数（1 首试 + N-1 次重试），默认 3。
	Retries int `yaml:"retries"`
	// RetryBase 首次重试前的退避基数，默认 100ms；按 RetryFactor 指数放大。
	RetryBase   Duration `yaml:"retry_base"`
	RetryFactor float64  `yaml:"retry_factor"`
	// AcquireTimeout 等待池信号量的上限，默认 30s；0/负数表示无限等待。
	AcquireTimeout Duration `yaml:"acquire_timeout"`
	// Cooldown 熔断冷却时长，到期自动恢复；默认 30s。
	Cooldown Duration `yaml:"cooldown"`
}

// allKeys 合并 api_key/key/keys 三种写法，去重保序；全空返回 nil。
func (pc PoolConfig) allKeys() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" {
			return
		}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	add(pc.Key)
	add(pc.APIKey)
	for _, k := range pc.Keys {
		add(k)
	}
	return out
}
