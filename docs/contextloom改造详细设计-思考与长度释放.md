# ContextLoom 改造详细设计：思考与长度释放

> **读者**：ContextLoom 核心开发者（model / config / runtime / modelgateway / tui / gui 维护者）。
> **目标**：把"释放思考长度、允许在设置里配置思考长度/输出最大长度/输入最大长度"展开为可直接排期实施的接口级设计。
> **范围**：三层参数模型、四个配置项、请求体发送矩阵、热更新链路、UI 接线；全部建立在与 DeepSeek Harness（dsh）的机制对标之上。
> **口径**：现状全部取自仓库实际源码（文件 + 函数名 + 约行号）；dsh 参照均注明包/文件；目标接口为完整 Go 定义，可直接落码。
> **关联**：《dsh能力释放机制深度分析.md》（机制归因；本文承接其第 5 节"上下文工程"）；《contextloom改造详细设计-P0架构级.md》（其 P0-1 的请求信封快照是本文参数的审计落点）；《contextloom改造详细设计-P1P2能力级.md》（P2-8 压缩锁与本文的 ctxBudget 分母语义变更正交，本文不重复其条目）。

## 0 总览

| 编号 | 改造项 | 一句话目标 | dsh 参照 | 依赖顺序 |
|---|---|---|---|---|
| T1 | 模型层生成参数接口 | 输出上限/输入预算/思考开关成为后端一等参数，请求体构造集中化 | `llm-deepseek/src/serialize.ts`、`llm-deepseek/src/model-info.ts` | 无（先做） |
| T2 | 配置层四配置项 | `models[]` 新增 `max_output_tokens/max_input_tokens/thinking_length/thinking_enabled` 并三处接线 | `llm-deepseek/src/config.ts`（Volatile 配置 + resolve 校验） | 无（与 T1 并行） |
| T3 | 运行时解析链与热更新 | 三层优先级解析、运行时 API、settings 持久化、ctxBudget 改用有效输入预算 | `llm/src/call-config.ts`（请求信封）、`llm-deepseek/src/types.ts`（每次操作重读） | T1、T2 |
| T4 | 网关透传 | `modelgateway.handle` 缓存并透传四参数，多实例轮换参数一致 | `llm/src/call-config.ts`（配置变更=信封变更） | T1 |
| T5 | UI 接线 | TUI `/effort` 扩展（off 档+长度参数）、GUI 设置面板字段、状态栏展示 | `llm-deepseek/src/model-info.ts`（efforts 列表驱动 UI） | T3 |
| T6 | 默认值与文档收口 | `config.example.yaml`、GUI 设计文档、注释欠账修正；默认值不切换 | `llm-deepseek/src/defaults.ts`（默认值集中声明） | T1–T5 |

**术语表（本文口径）**

- **contextWindow（maxContext）**：模型单次请求的"输入 + 输出"共享总量上限（ContextLoom 落点：`ModelPreset.MaxContext`）。
- **max_output_tokens（输出上限）**：单次请求 `max_tokens` 的显式值；0 = 未配置。
- **max_input_tokens（输入预算）**：发送前压缩的目标预算；0 = 未配置。
- **有效输入预算（effective input budget）**：经输出预留收紧后的实际压缩分母（公式见 §2.3④）。
- **sendEffort**：既有总开关（`DeepSeekBackend.sendEffort`），控制是否发送任何思考相关字段。
- **thinking 三态**：`nil` = 不显式发送 `thinking` 字段；`true` = 发 `thinking:{type:"enabled"}`；`false` = 发 `thinking:{type:"disabled"}`。
- **覆盖（override）**：运行时会话级显式值（settings.json 持久），优先级最高。
- **写透**：运行时 setter 同步更新激活预设的内存副本（沿用 `/top_p` 既有模式）。

**依赖与顺序**：T1、T2 无前置可并行；T3 依赖 T1+T2；T4 依赖 T1；T5 依赖 T3；T6 收尾。每步独立可交付、可回滚（见 §5）。

---

## 1 问题定义：把"思考长度释放"操作化

### 1.1 三个被混淆的量

ContextLoom 现状把三个本应独立的量混同为一：

| 量 | 含义 | ContextLoom 现状落点（源码证据） |
|---|---|---|
| 模型上下文窗口 | 输入 + 输出共享总量上限 | `ModelPreset.MaxContext`；`ctxmgr.TokenBudget.MaxTokens`（`internal/ctxmgr/budget.go` 字段注释："模型上下文窗口上限（= cfg.Model.MaxContext）"） |
| 单次输出上限 | 请求体 `max_tokens` | `internal/model/deepseek.go` `maxTokens()`（约 81-88 行）：`limit := d.maxContext; ... return limit` —— 输出 = 窗口 |
| 单次输入预算 | 发送前压缩目标 | `deepseek.go` `Chat/StreamEx` 发送前兜底 `ctxmgr.NewTokenBudget(d.maxContext)`；`runtime.go` `New()` 中 `rt.ctxBudget = ctxmgr.NewTokenBudget(cfg.ActiveModel().MaxContext)` |

**后果①：输出长度不可独立配置。** `maxTokens()` 的唯一输入是 `maxContext`；用户为"长输入"调大窗口（如 config.example.yaml 示例 `max_context: 1000000`）→ 输出预算被动放大，服务端的预留、计费与 400 阶梯钳制行为全部随窗口漂移；反过来想加大输出，也被迫加大窗口。

**后果②：无法关闭思考（低成本任务被迫付思考成本）。** `deepseek.go` 的 `deepseekEffortRemap` 只有五档（low→low、medium→high、high→high、xhigh→max、max→max），**缺 off 档**；`SetReasoningEffort`（约 897-903 行）对表外值原样透传——硬传 `"off"` 只会把 `reasoning_effort:"off"` 原样发给服务端（是否合法取决于服务端），并没有"关闭思考"的显式协议表达。格式转换、简单检索、机械编辑等任务必须支付思考 token 成本。

**后果③：无法为"长思考"预留输出空间。** `deepseek.go` `StreamChunk.FinishReason` 注释自述："length=模型输出被 max_tokens 截断（长思考耗尽预算后工具调用被腰斩）"。思考 token 与正文共享同一 `max_tokens`：思考越长，正文/工具调用越容易被截断；而用户当前唯一杠杆是调大 `max_context`（又牵连后果①）。

三个后果的共同根因：**"一个标量管三件事"**——这正是 dsh 与 ContextLoom 在生成参数上的代差所在。

### 1.2 dsh 的机制答案（源码核实）

| 机制 | dsh 源码证据 | 要点 |
|---|---|---|
| 三级优先级（调用级 > 模型元数据 > 连接默认） | `packages/llm/llm-deepseek/src/serialize.ts`（约 140-160 行）：`max_tokens: options.maxTokens ?? model?.maxTokens ?? connection.maxTokens`；effort 同构：`options.reasoningEffort ?? (connection.defaults.reasoningEffort ?? (connection.defaults.thinking === 'disabled' ? 'off' : 'high'))` | 输出上限有独立解析链，与 contextWindow 从不耦合 |
| thinking 独立字段 + off 档 | `serialize.ts`：`thinking: { type: effort === 'off' ? 'disabled' : 'enabled' }`，且 `...effort === 'off' ? {} : { output_config: { effort } }`；`model-info.ts`：`REASONING_EFFORTS` 四档（off/low/high/max，带 name/description 供选择器渲染），部署策略 `thinking='disabled'` 时降为 `OFF_ONLY_REASONING_EFFORTS`（唯一档 off，defaultEffort=off） | 关闭思考是**协议层显式声明**（thinking:disabled），且关闭时不发送矛盾参数（不发 effort） |
| 配置层互斥校验 | `config.ts` `resolveAdapterOptions`：`thinking === 'disabled' && reasoningEffort !== undefined && reasoningEffort !== 'off'` → 抛错 "only reasoningEffort 'off' can be configured when thinking is disabled"；`serialize.ts` 同规则抛 `UNSUPPORTED_REASONING_EFFORT` | "关闭思考"与"发送思考强度"不允许自相矛盾的配置共存 |
| 默认值分离 | `defaults.ts`：`DEFAULT_CONTEXT_WINDOW = 1_000_000` 与 `DEFAULT_MAX_TOKENS = 256_000` 是两个独立常量 | 输出默认值远小于窗口——二者独立，绝不互推 |
| 请求信封化（配置变更即信封变更，可审计） | `packages/llm/llm/src/call-config.ts`：`LlmCallConfig { provider, model, reasoningEffort, temperature, maxTokens, stop }` + `callConfigEquals` 字段级比较；模块注释："reasoning effort, and sampling values are request-header state that can affect cache reuse; request waterfalls replace them and the loop logs changed snapshots instead of allowing silent per-call drift." `llm-deepseek/src/types.ts`："re-reads it per operation, which is what makes a configuration change reach the next request without re-registration." | 生成参数属于"请求头状态"，变更被记录、可审计；每次操作重读配置，改配置无需重新注册 |
| 配置载体语义（Volatile） | `config.ts`：`thinking: Volatile<'enabled'|'disabled'|undefined>`、`reasoningEffort: Volatile<'off'|'low'|'high'|'max'|undefined>`、`maxTokens: Volatile<number>`，schema 全部 `.volatile()` | 配置是"活引用"，读取时取当前值 |

**协议差异注记（如实描述）**：dsh 的 DeepSeek 适配器走 Anthropic Messages 协议，请求体字段是 `thinking:{type}` 与 `output_config:{effort}`（并非 OpenAI 的 `reasoning_effort`），且**每次请求都显式发送** `thinking` 字段。ContextLoom 走 OpenAI 兼容 `/chat/completions`，现状字段是 `reasoning_effort`；本次新增 `thinking` 字段采用"仅在显式配置时发送（nil=不发）"策略，避免破坏严格校验未知字段的兼容端点——这是有意的偏离，风险与兜底见 §6。

### 1.3 本文档的边界

**做**：四个配置项、三层解析链、请求体发送矩阵、运行时热更新、UI 接线、零回归与解耦不变式。

**不做（明确清单）**：

1. 不改 400 自修复阶梯（`parseMaxTokensLimit` / `nextMaxTokensCap` / `maxTokensCapLadder` 原样保留，作为最后防线）；
2. 不改压缩算法本体（`ctxmgr.CompactToBudgetWithEvict` 四步渐进压缩原样保留，只改预算分母的来源）；
3. 不引入 Anthropic Messages 协议（`output_config` 不落地）；
4. 不做思考 token 的独立计量/计费（服务端未承诺 usage 拆分字段前不做本地猜测）；
5. 不涉及 P0/P1/P2 既有条目（事件溯源 / Seam / 沙箱 / LSP / 工作流等）；
6. 不切换任何默认值（默认 = 现状，释放的是"上限"而不是"新行为"）。

---

## 2 目标设计：三层参数模型

### 2.1 三层配置的语义与优先级

| 层级 | 存储 | 覆盖字段 | 写入路径 | 语义 |
|---|---|---|---|---|
| L1 模型预设级 | `config.yaml` `models[]` | `max_output_tokens` / `max_input_tokens` / `thinking_length` / `thinking_enabled` | GUI"模型"分组、手工编辑、`ApplyConfigPatch` | 随预设持久；模型的"出厂参数" |
| L2 CLI 默认级 | `config.yaml` `cli_defaults` | `effort`（枚举扩展 off） | GUI"CLI 默认"分组、手工编辑 | 全局默认思考长度；长度参数**不设 L2**（避免配置面膨胀，长度天然跟随模型/端点） |
| L3 运行时会话覆盖 | settings.json（项目 `.contextloom/`） | `thinking_length` / `thinking_enabled` / `max_output_tokens` / `max_input_tokens`（+ `effort` 兼容键） | `/effort`、运行时 API、GUI 保存即时值 | 用户显式调整的最近值；进程内立即生效、重启恢复 |

**优先级：L3 > L1 > L2（仅思考长度）> 内置默认。**

**逐条解析规则与回退链**：

| 生效值 | 解析链（取第一个非缺省值） | 缺省/兜底 |
|---|---|---|
| 思考长度 `thinkingLength` | L3.thinking_length → L1.ThinkingLength → L2.CLIDefaults.Effort → `"max"` | `"max"` 与现状 `applyDefaults`（config.go："思考深度默认 max：未配置时使用最高推理强度"）一致 |
| 输出上限 `maxOutputTokens` | L3 → L1 → `0` | 0 = 未配置，`maxTokens()` 回落 `maxContext`（现状） |
| 输入预算 `maxInputTokens` | L3 → L1 → `0` | 0 = 未配置，压缩预算回落 `maxContext`（现状） |
| 思考开关 `thinkingEnabled` | L3 → L1 → `nil` | nil = 不显式发送 `thinking` 字段（现状） |

**覆盖清除规则（防"陈旧覆盖"锁死新配置）**：

1. `/effort auto` 或运行时 API 传空值（0/""/nil）→ 清除对应覆盖；
2. GUI 保存 `cli_defaults.effort`（L2）→ 清除思考长度覆盖（"保存新默认"= 新意图，应完整生效）；
3. GUI 保存模型预设四字段（`config.set model.*`，L1）→ 清除对应覆盖；
4. 切换激活模型预设（`SwitchModel` / `AddModelPreset` / `RemoveModelPreset`）→ 清除全部四类覆盖（预设是模型参数的完整声明，切换 = 采用新声明）。

**零回归论证**：旧用户没有新键覆盖（`loadSettings` 迁移规则见 §3.4），规则 4 对旧用户是空操作；`/effort` 同时写 L2（与现状 `SetEffort` 改 `CLIDefaults.Effort` 的行为一致），清除思考覆盖后回落 L2 = 用户最近值，行为与现状一致。

**为什么 L1 > L2**：`models[]` 已是模型配置的唯一定义处（`config.go` 注释："模型配置唯一定义在 models 列表……杜绝双份配置漂移"）；预设显式值若被全局默认压掉，预设级功能即形同虚设。

**为什么 L3 最高**：覆盖是"用户刚刚显式调整"的直接证据，即时交互必须立刻见效；持久配置保存时清除覆盖，保证新配置完整生效。

### 2.2 四个配置项定义

| 字段 | YAML key | 类型 | 取值范围 | 缺省语义（0/nil/""） | 非法值处理 |
|---|---|---|---|---|---|
| `MaxOutputTokens` | `model.max_output_tokens` | int | ≥ 0；0 = 未配置 | 0 → `max_tokens = maxContext`（现状） | < 0 拒绝（applyOne/Validate）；> maxContext 允许保存但 UI 警告（服务端裁决 + 400 阶梯兜底） |
| `MaxInputTokens` | `model.max_input_tokens` | int | ≥ 0；0 = 未配置 | 0 → 压缩预算 = maxContext（现状） | 同左 |
| `ThinkingLength` | `model.thinking_length` | string | `""`（未配置）/ `off` / `low` / `medium` / `high` / `xhigh` / `max` | `""` → 回退链（§2.1） | 枚举外拒绝；三处一致：`Validate`、`applyOne`（config.set 返回错误）、运行时 API（返回错误并保持原值） |
| `ThinkingEnabled` | `model.thinking_enabled` | *bool | `nil`（未配置）/ `true` / `false` | `nil` → 不显式发送 `thinking` 字段（现状） | 类型错误拒绝；GUI 用 `auto/on/off` 三态 select 映射 nil/true/false |

**统一约定：零值 = 未配置 = 回退**。不引入"显式 0"的第二种含义（避免歧义）；"清除配置"操作统一走空值（0/""/nil/"auto"）。

**档位 → API 映射**（沿用 `deepseekEffortRemap`，新增 off）：

| 档位 | 请求体行为 |
|---|---|
| off | `thinking:{type:"disabled"}`；**不发** `reasoning_effort` |
| low | `reasoning_effort:"low"` |
| medium / high | `reasoning_effort:"high"` |
| xhigh / max | `reasoning_effort:"max"` |

### 2.3 关键不变式

**① 零回归**：四字段全未配置时——请求体 `max_tokens == maxContext`；发送前压缩预算 `== maxContext`；`ctxBudget.MaxTokens == maxContext`；请求体不含 `thinking`；`reasoning_effort` 发送逻辑与现状逐字一致（`if d.sendEffort && d.reasoningEffort != ""`）。

**② 输出与输入解耦**：`max_tokens` 只由 `max_output_tokens`/`maxContext` 决定；压缩预算只由 `max_input_tokens`/`maxContext` 决定；互不推导。

**③ 一致性（对齐 dsh 互斥校验）**：思考关闭（effort=off 或 `thinking_enabled=false`）⇒ 请求体不含 `reasoning_effort`、含 `thinking:{type:"disabled"}`；配置层 `Validate` 拒绝 `thinking_enabled=false` 与 `thinking_length ∈ {low..max}` 的矛盾组合（对齐 dsh `resolveAdapterOptions`）；运行时 API 遇矛盾组合以 `thinking_enabled=false` 优先（不打断交互）并在状态栏提示。

**④ 钳制规则（防无界膨胀）**：

- 各自钳制：有效输出 = `min(maxOutputTokens>0 ? maxOutputTokens : maxContext, maxContext)`；输入 base = `min(maxInputTokens>0 ? maxInputTokens : maxContext, maxContext)`。
- 预留收紧（**仅当两参数同时显式配置**）：`reserve = min(有效输出, maxContext)`；`ceiling = maxContext − reserve`；若 `ceiling < maxContext/10` 则 `ceiling = maxContext/10`（下限保护，防输入预算归零）；有效输入预算 = `min(base, ceiling)`，且 ≥ 1。
- 为什么：上下文窗口是"输入 + 输出"的共享总量；两者同时显式配置时，客户端必须把两者之和压回窗口内，否则请求必然 400（靠 400 自修复兜底会多一次往返）；10% 下限保证极端配置（如 out = maxContext）下仍能对话，并在 UI 标注警告。
- 未同时配置时不做收紧（保持现状语义：单边配置不影响另一边）。

**有效输入预算纯函数（公式唯一来源，防 runtime 与 model 双实现漂移）**：

```go
// internal/model/budget.go（新增）
package model

// EffectiveInputBudget 计算"有效输入预算"：发送前压缩与 runtime.ctxBudget 共用的分母。
// 规则（与 §2.3④ 一一对应）：
//  1. base = maxInput>0 ? maxInput : maxContext
//  2. base = min(base, maxContext)
//  3. 仅当 maxInput>0 且 maxOutput>0 时做输出预留收紧：
//     reserve = min(maxOutput, maxContext)
//     ceiling = maxContext - reserve
//     if ceiling < maxContext/10 { ceiling = maxContext/10 }
//     base = min(base, ceiling)
//  4. 返回值 ≥ 1；maxContext<=0 时返回 0（无预算限制，与 NewTokenBudget 语义一致）
func EffectiveInputBudget(maxContext, maxInput, maxOutput int) int {
	if maxContext <= 0 {
		return 0
	}
	base := maxContext
	if maxInput > 0 {
		base = maxInput
	}
	if base > maxContext {
		base = maxContext
	}
	if maxInput > 0 && maxOutput > 0 {
		reserve := maxOutput
		if reserve > maxContext {
			reserve = maxContext
		}
		ceiling := maxContext - reserve
		if floor := maxContext / 10; ceiling < floor {
			ceiling = floor
		}
		if base > ceiling {
			base = ceiling
		}
	}
	if base < 1 {
		base = 1
	}
	return base
}
```

---

## 3 接口级设计

### 3.1 internal/model 层：GenerationParamsBackend 可选接口

```go
// internal/model/backend.go（新增可选接口，紧邻 StreamExBackend 定义）

// GenerationParamsBackend 可选接口：支持"生成参数"（输出上限 / 输入预算 /
// 思考开关）的热更新与回读。DeepSeekBackend 原生实现；modelgateway.handle
// 缓存透传并自身实现——agent/runtime 在网关路由路径下同样能设置参数。
//
// 为什么不做进 Backend 接口本体：Backend 被大量测试 mock（executor /
// modelgateway / runtime 的 *_test.go 直接实现）与包外实现方消费；扩本体会
// 全量破坏编译面。沿用 StreamExBackend 的既有模式（可选接口 + 类型断言 +
// handle 透传；该模式已在"网关包装导致类型断言失败 → 工具调用被吞"的历史
// 缺陷修复中验证过必要性）。
type GenerationParamsBackend interface {
	SetMaxOutputTokens(n int) // 0=清除覆盖，回退 maxContext
	MaxOutputTokens() int
	SetMaxInputTokens(n int) // 0=清除覆盖，回退 maxContext
	MaxInputTokens() int
	SetThinkingEnabled(v *bool) // nil=不显式发送 thinking 字段
	ThinkingEnabled() *bool
}
```

**设计决定**：思考长度的热更新**不新增接口方法**，走既有 `Backend.SetReasoningEffort`（新增 off 档语义，见 §3.2）——与并行实现的子任务 B 口径一致（其接口清单同样只有 6 个方法）。

### 3.2 DeepSeekBackend：字段、方法与请求体构造

**新增字段与锁约定**：

```go
type DeepSeekBackend struct {
	// ... 既有字段（endpoint/apiKey/modelID/modelMu/maxContext/client/
	//     reasoningEffort/sendEffort/streamTools/topP/temperature/supportsVision）不变
	maxOutputTokens int   // 单次输出上限；0=未配置（maxTokens() 回落 maxContext）
	maxInputTokens  int   // 单次输入预算；0=未配置（inputLimit() 回落 maxContext）
	thinkingEnabled *bool // 三态；nil=不显式发送 thinking 字段
}
```

锁约定：新字段读写走既有 `modelMu`（与 `SwitchModel` / `SetMaxContext` / `SetSendEffort` 同锁；`topP/temperature` 未上锁是既有欠账，不在本次扩散）。

**maxTokens() 新语义**（maxOutputTokens > 0 优先，否则 maxContext）：

```go
// maxTokens 计算单次模型输出上限：显式输出上限优先，否则取 maxContext（现状）。
// 不额外钳制显式值——沿用"把'什么上限'的裁决交给服务端"的既有哲学（见字段注释），
// 400 自修复路径（parseMaxTokensLimit / nextMaxTokensCap）原样保留，作为最后防线。
func (d *DeepSeekBackend) maxTokens() int {
	d.modelMu.Lock()
	out, ctx := d.maxOutputTokens, d.maxContext
	d.modelMu.Unlock()
	if out > 0 {
		return out
	}
	if ctx < 1 {
		return 1
	}
	return ctx
}
```

**inputLimit()**（供发送前压缩；与 runtime 的 ctxBudget.MaxTokens 同源同式）：

```go
// inputLimit 返回发送前压缩使用的有效输入预算（公式唯一来源：
// model.EffectiveInputBudget，runtime 侧共用同一函数）。
func (d *DeepSeekBackend) inputLimit() int {
	d.modelMu.Lock()
	defer d.modelMu.Unlock()
	return EffectiveInputBudget(d.maxContext, d.maxInputTokens, d.maxOutputTokens)
}
```

**请求体构造集中化 + thinking 发送矩阵**：现状 `Chat` 与 `StreamEx` 各自手写一遍 `body` 构造与 effort 发送逻辑（deepseek.go 两处），本次收敛为一个共用方法：

```go
// applyGenerationParams 把生成参数写入请求体（Chat 与 StreamEx 共用，防两处漂移）。
// thinking 发送矩阵见设计文档 §3.2 表；sendEffort 为总开关。
func (d *DeepSeekBackend) applyGenerationParams(body map[string]any) {
	body["max_tokens"] = d.maxTokens()

	d.modelMu.Lock()
	sendEffort, effort, te := d.sendEffort, d.reasoningEffort, d.thinkingEnabled
	d.modelMu.Unlock()

	if !sendEffort {
		return // 总开关：reasoning_effort 与 thinking 都不发送（现状语义）
	}
	off := effort == "off" || (te != nil && !*te)
	if off {
		// 关闭思考：发显式 disabled 声明，且不发 reasoning_effort（互斥，对齐 dsh）
		body["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	if te != nil && *te {
		body["thinking"] = map[string]any{"type": "enabled"}
	}
	if effort != "" {
		body["reasoning_effort"] = effort
	}
}
```

**发送矩阵（sendEffort × thinkingEnabled × reasoningEffort）**：

| sendEffort | thinkingEnabled | reasoningEffort | 请求体（除既有字段外） | 与现状关系 |
|---|---|---|---|---|
| true | nil | "" | 无 thinking / 无 effort | 现状（逐字一致） |
| true | nil | 非空非 off | `reasoning_effort=<remap 值>` | 现状（逐字一致） |
| true | nil | off | `thinking:{type:"disabled"}`，无 effort | 新增（off 档） |
| true | true | 非空非 off | `thinking:{type:"enabled"}` + `reasoning_effort` | 新增 |
| true | false | 任意 | `thinking:{type:"disabled"}`，无 effort | 新增 |
| false | 任意 | 任意 | 两者都不发 | 现状（逐字一致） |

**deepseekEffortRemap 增加 off**：

```go
var deepseekEffortRemap = map[string]string{
	"off":    "off", // 关闭思考：作为发送矩阵的关闭信号原样保留，不映射为服务端档位
	"low":    "low",
	"medium": "high",
	"high":   "high",
	"xhigh":  "max",
	"max":    "max",
}
```

`SetReasoningEffort` 保持"表内 remap、表外透传"的既有实现；`"off"` 因进表而原样保留；发送矩阵按 `effort == "off"` 判定关闭。**`SetReasoningEffort("off")` 等效关闭思考**（子任务 B 口径）。

**发送前压缩改造**（两处：`Chat`/`StreamEx` 的"发送前兜底②"与 400 分支的压缩兜底）：

```go
// 旧：ctxmgr.NewTokenBudget(d.maxContext)
// 新：预算分母改用有效输入预算
if lim := d.inputLimit(); lim > 0 && ctxmgr.NewTokenBudget(lim).SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
	if compacted, _ := ctxmgr.CompactToBudget(messages, ctxmgr.NewTokenBudget(lim)); compacted != nil {
		messages = compacted
	}
}
```

**400 自修复阶梯：保持不变**（`parseMaxTokensLimit` 优先、`nextMaxTokensCap` 阶梯兜底、每次 400 允许继续降级）。

### 3.3 internal/config 层：四字段与三处接线

**ModelPreset 新字段**（`internal/config/config.go`）：

```go
type ModelPreset struct {
	Name           string   `yaml:"name"`
	Backend        string   `yaml:"backend"`
	Endpoint       string   `yaml:"endpoint"`
	APIKey         string   `yaml:"api_key"`
	MaxContext     int      `yaml:"max_context"`
	TopP           *float64 `yaml:"top_p"`
	Temperature    *float64 `yaml:"temperature"`
	VisionModel    string   `yaml:"vision_model"`
	SupportsVision bool     `yaml:"supports_vision"`
	SendEffort     *bool    `yaml:"send_effort"`
	// ── 生成参数（本次新增；零值=未配置=回退，见设计文档 §2.2）──
	MaxOutputTokens int    `yaml:"max_output_tokens"` // 单次输出上限；0=max_tokens 取 max_context（现状）
	MaxInputTokens  int    `yaml:"max_input_tokens"`  // 单次输入预算；0=压缩预算取 max_context（现状）
	ThinkingLength  string `yaml:"thinking_length"`   // ""|off|low|medium|high|xhigh|max；""=回退
	ThinkingEnabled *bool  `yaml:"thinking_enabled"`  // nil=不显式发送 thinking 字段（现状）
}
```

**接线① ConfigSchema()**（模型分组新增 4 条；select 空枚举项约定：首项 `""` 渲染为"未设置（回退默认）"）：

```go
{Key: "model.max_output_tokens", Label: "输出上限 (tokens)", Type: "number", Group: "模型", Default: 0,
	Desc: "单次输出上限；0=沿用最大上下文（现状）。长思考任务建议预留思考余量",
	Min: f64p(0), Max: f64p(100000000), Step: 1024},
{Key: "model.max_input_tokens", Label: "输入预算 (tokens)", Type: "number", Group: "模型", Default: 0,
	Desc: "发送前压缩目标预算；0=沿用最大上下文（现状）",
	Min: f64p(0), Max: f64p(100000000), Step: 1024},
{Key: "model.thinking_length", Label: "思考长度", Type: "select", Group: "模型", Default: "",
	Enums: []string{"", "off", "low", "medium", "high", "xhigh", "max"},
	Desc: "off=关闭思考；空=回退全局默认；low→low，medium/high→high，xhigh/max→max"},
{Key: "model.thinking_enabled", Label: "思考开关", Type: "select", Group: "模型", Default: "auto",
	Enums: []string{"auto", "on", "off"},
	Desc: "auto=不显式发送 thinking 字段（默认）；on/off=显式发送 thinking:{type}"},
```

同时扩展 `cli_defaults.effort` 枚举并修正描述：

```go
{Key: "cli_defaults.effort", Label: "思考深度", Type: "select", Group: "CLI 默认", Default: "max",
	Enums: []string{"off", "low", "medium", "high", "xhigh", "max"},
	Desc: "全局默认思考长度（off=关闭思考；low→low，medium/high→high，xhigh/max→max）"},
```

**接线② Sanitized()**：

```go
m["model.max_output_tokens"] = am.MaxOutputTokens
m["model.max_input_tokens"] = am.MaxInputTokens
m["model.thinking_length"] = am.ThinkingLength
m["model.thinking_enabled"] = triStateString(am.ThinkingEnabled) // nil→"auto", true→"on", false→"off"
```

**接线③ Apply 链路（ApplyPatch/applyOne）**（沿用 `setIntPreset` 写透激活预设的既有模式）：

```go
case "model.max_output_tokens":
	return setIntPreset(c, key, val, func(m *ModelPreset, n int) {
		if m != nil { m.MaxOutputTokens = clampNonNeg(n) } // <0 钳到 0（=未配置）
	})
case "model.max_input_tokens":
	return setIntPreset(c, key, val, func(m *ModelPreset, n int) {
		if m != nil { m.MaxInputTokens = clampNonNeg(n) }
	})
case "model.thinking_length":
	s, ok := val.(string)
	if !ok {
		return fmt.Errorf("%s 需要字符串", key)
	}
	if s != "" && !validThinkingLength(s) {
		return fmt.Errorf("%s 非法档位 %q（允许: off/low/medium/high/xhigh/max）", key, s)
	}
	writePreset(func(m *ModelPreset) { if m != nil { m.ThinkingLength = s } })
	return nil
case "model.thinking_enabled":
	v, err := parseTriState(val) // "auto"→nil, "on"/true→true, "off"/false→false
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	writePreset(func(m *ModelPreset) { if m != nil { m.ThinkingEnabled = v } })
	return nil
```

**applyDefaults()：不为四个新字段设默认**（零值即"未配置"，保持零回归；仅加注释说明）。

**Validate() 新增规则**：

```go
if am.ThinkingLength != "" && !validThinkingLength(am.ThinkingLength) {
	return fmt.Errorf("model.thinking_length 非法档位 %q（允许: off/low/medium/high/xhigh/max）", am.ThinkingLength)
}
if am.ThinkingEnabled != nil && !*am.ThinkingEnabled && am.ThinkingLength != "" && am.ThinkingLength != "off" {
	return fmt.Errorf("model.thinking_enabled=false 时 model.thinking_length 仅允许 off 或空（对齐 dsh 互斥校验）")
}
if am.MaxOutputTokens < 0 || am.MaxInputTokens < 0 {
	return fmt.Errorf("model.max_output_tokens/max_input_tokens 不能为负")
}
```

### 3.4 internal/runtime 层：解析链、持久化与热更新

**会话覆盖结构**（Runtime 新字段）：

```go
// generationOverride 会话级生成参数覆盖（nil=未覆盖；settings.json 持久）。
type generationOverride struct {
	thinkingLength  *string // nil=未覆盖；值域同 §2.2
	thinkingEnabled *bool   // 注意：nil 是合法覆盖值（=不发 thinking 字段），用 thinkingSet 区分"未覆盖"
	thinkingSet     bool
	maxOutputTokens *int
	maxInputTokens  *int
}
```

**settings.json 键与迁移规则**：

| 键 | 语义 | 写入者 | 读取规则（loadSettings） |
|---|---|---|---|
| `thinking_length` | 思考长度覆盖（新键） | `/effort`、`SetThinkingLength` | 键存在 → 建立覆盖 |
| `effort` | 旧键（降级兼容） | 同上（仅当值 ∈ 五档子集） | **无 `thinking_length` 键时：不建覆盖**，值写入 `cli_defaults.effort`（= 旧版语义的全局默认；保证旧用户零回归，且不挡住新的预设级字段） |
| `max_output_tokens` / `max_input_tokens` | 长度覆盖 | 运行时 API、GUI | 存在（>0）→ 建立覆盖 |
| `thinking_enabled` | 三态覆盖 | 运行时 API、GUI | 存在（true/false）→ 建立覆盖 |

**为什么 effort 键"条件写入"**：旧版二进制读新 settings 时只看 `effort` 键；若写入 `"off"`，旧版 remap 表无 off → 原样透传为 `reasoning_effort:"off"`，存在 400 风险。因此：覆盖值为 off/"" 时不写 `effort` 键（旧版回落 config 默认）；值为五档子集时写镜像，旧版行为正常。

```go
// saveSettings 追加（既有键全部保留）：
if ov := rt.genOverride; ov.thinkingLength != nil {
	settings["thinking_length"] = *ov.thinkingLength
	if isLegacyEffortValue(*ov.thinkingLength) { // {low,medium,high,xhigh,max}
		settings["effort"] = *ov.thinkingLength // 旧版兼容镜像；off/"" 不写
	}
} else {
	settings["effort"] = rt.Config.CLIDefaults.Effort // 现状行为
}
if ov.thinkingSet {
	settings["thinking_enabled"] = *ov.thinkingEnabled
}
if ov.maxOutputTokens != nil {
	settings["max_output_tokens"] = *ov.maxOutputTokens
}
if ov.maxInputTokens != nil {
	settings["max_input_tokens"] = *ov.maxInputTokens
}
```

**运行时 API（四个，命名与子任务口径一致）**：

```go
// SetMaxOutputTokens 设置会话级输出上限覆盖（0=清除覆盖，回退预设/现状）。
func (rt *Runtime) SetMaxOutputTokens(n int) {
	rt.genOverride.maxOutputTokens = clampNil(n) // n==0 → nil（清除）
	rt.applyGenerationParams()
	rt.saveSettings()
}

// SetMaxInputTokens 设置会话级输入预算覆盖（0=清除）。
func (rt *Runtime) SetMaxInputTokens(n int)

// SetThinkingLength 设置会话级思考长度覆盖（""/"auto"=清除）。
// 合法档位 off/low/medium/high/xhigh/max；非法档位返回错误并保持原值。
func (rt *Runtime) SetThinkingLength(level string) error

// SetThinkingEnabled 设置会话级思考开关覆盖（nil=清除）。
func (rt *Runtime) SetThinkingEnabled(v *bool)
```

**统一热应用出口（单一函数，防多处漂移）**：

```go
// applyGenerationParams 解析三层优先级并下发到后端 + 重算上下文预算。
// 所有写入路径（/effort、运行时 API、ApplyConfigPatch、SwitchModel、
// 启动初始化）最终都汇聚到这里。
func (rt *Runtime) applyGenerationParams() {
	am := rt.Config.ActiveModel()
	out := rt.resolveMaxOutputTokens(am) // 覆盖 > 预设 > 0
	in := rt.resolveMaxInputTokens(am)   // 覆盖 > 预设 > 0
	th := rt.resolveThinkingLength(am)   // 覆盖 > 预设 > cli_defaults.effort > "max"
	te := rt.resolveThinkingEnabled(am)  // 覆盖 > 预设 > nil

	if rt.Backend != nil {
		rt.Backend.SetReasoningEffort(th) // 既有方法；off 档由模型层发送矩阵处理
		if gp, ok := rt.Backend.(model.GenerationParamsBackend); ok {
			gp.SetMaxOutputTokens(out)
			gp.SetMaxInputTokens(in)
			gp.SetThinkingEnabled(te)
		}
	}
	// 上下文预算分母 = 有效输入预算（与 model.EffectiveInputBudget 同式）
	rt.historyMu.Lock()
	if rt.ctxBudget != nil {
		rt.ctxBudget.MaxTokens = model.EffectiveInputBudget(am.MaxContext, in, out)
	}
	rt.historyMu.Unlock()
}

// ApplyGenerationParamsTo 对任意后端实例应用当前生效参数（网关句柄路径）。
func (rt *Runtime) ApplyGenerationParamsTo(b model.Backend) {
	am := rt.Config.ActiveModel()
	b.SetReasoningEffort(rt.resolveThinkingLength(am))
	if gp, ok := b.(model.GenerationParamsBackend); ok {
		gp.SetMaxOutputTokens(rt.resolveMaxOutputTokens(am))
		gp.SetMaxInputTokens(rt.resolveMaxInputTokens(am))
		gp.SetThinkingEnabled(rt.resolveThinkingEnabled(am))
	}
}
```

**网关句柄应用点**（`selectBackend` 一处包装，覆盖所有调用点）：

```go
func (rt *Runtime) selectBackend(ctx context.Context, req modelgateway.Req) model.Backend {
	if rt.Gateway == nil {
		return rt.Backend
	}
	b, err := rt.Gateway.Select(ctx, req)
	if err != nil {
		return rt.Backend
	}
	rt.ApplyGenerationParamsTo(b) // 网关句柄缓存透传（池内轮换参数一致）
	return b
}
```

**New() 初始化顺序（在既有步骤上追加）**：

1. 后端构造（现状，`sendEffort` 从预设读取）；
2. `loadSettings()`（现状；新增覆盖恢复与 effort 键迁移）；
3. **新增**：`applyGenerationParams()` 取代现有"effort 默认 max 下发"两行（`rt.Config.CLIDefaults.Effort == "" → "max"` 兜底逻辑上移至 `resolveThinkingLength` 的末级）；
4. ctxBudget 初始化：`NewTokenBudget(model.EffectiveInputBudget(am.MaxContext, in, out))`（替代现状 `NewTokenBudget(cfg.ActiveModel().MaxContext)`；未配置时两式结果相同）；
5. `applyLiveConfig`（现状触发条件不变，内部改走新出口）。

**applyLiveConfig 扩展**：

```go
// 1) 上下文预算热重建：MaxTokens 改用有效输入预算（保留滚动摘要防失忆——现状逻辑不变）
am := rt.Config.ActiveModel()
in, out := rt.resolveMaxInputTokens(am), rt.resolveMaxOutputTokens(am)
rt.ctxBudget = ctxmgr.NewTokenBudget(model.EffectiveInputBudget(am.MaxContext, in, out))
// 2) 模型后端热更新：既有块（SetReasoningEffort/TopP/Temperature/SwitchModel/
//    SetSupportsVision/SetEndpoint/SetAPIKey/SetMaxContext/SetSendEffort）之后，
//    追加 rt.applyGenerationParams()（统一出口，含新四参数）
```

**restartKeys 不新增**：四新字段全部热生效（applyLiveConfig 覆盖），不列入需重启清单。

**ConfigSummary()/状态展示扩展**：现有"思考深度: %s"行扩展为"思考: <档位>（来源: 覆盖/预设/CLI 默认/内置）| 输出上限: <n|max_context> | 输入预算: <n|max_context>"。

### 3.5 internal/modelgateway 层：handle 缓存与透传

```go
type handle struct {
	// ... 既有字段（gw/pool/targetName/degraded/notice/setMu/temp/topP/effort/streamTools）不变
	// ── 生成参数缓存（本次新增；applyParams 透传到池内实例）──
	maxOutputTokens *int
	maxInputTokens  *int
	thinkingEnabled *bool
	thinkingSet     bool // thinkingEnabled 本身可空，需独立标记"是否设置过"
}

// 编译期断言（新增第二条）
var (
	_ model.Backend                 = (*handle)(nil)
	_ model.GenerationParamsBackend = (*handle)(nil)
)

func (h *handle) SetMaxOutputTokens(n int) {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	v := n
	h.maxOutputTokens = &v
}
func (h *handle) MaxOutputTokens() int {
	h.setMu.Lock()
	defer h.setMu.Unlock()
	if h.maxOutputTokens != nil {
		return *h.maxOutputTokens
	}
	if gp, ok := h.probe().inner.(model.GenerationParamsBackend); ok {
		return gp.MaxOutputTokens()
	}
	return 0
}
// SetMaxInputTokens/MaxInputTokens、SetThinkingEnabled/ThinkingEnabled 同构。

// applyParams 追加（既有 temp/topP/effort/streamTools 之后）：
if gp, ok := ins.inner.(model.GenerationParamsBackend); ok {
	if h.maxOutputTokens != nil {
		gp.SetMaxOutputTokens(*h.maxOutputTokens)
	}
	if h.maxInputTokens != nil {
		gp.SetMaxInputTokens(*h.maxInputTokens)
	}
	if h.thinkingSet {
		gp.SetThinkingEnabled(h.thinkingEnabled)
	}
}
```

`nilBackend` 同步补零值实现（Set 空操作 / Get 返回 0、nil）。

**为什么**：handle 是 per-agent 句柄，池内实例轮换；参数缓存在句柄上、每次挑中实例后应用，保证同一 Agent 跨实例参数一致（沿用既有 `temp/topP/effort` 模式）。不实现该接口会让网关路径静默丢失参数——与 `StreamEx` 曾发生的"类型断言失败 → 工具调用被吞"是同类缺陷，故以编译期断言锁死。

### 3.6 UI 接线

**TUI `/effort` 扩展**（`internal/tui/commands.go`）：

| 命令 | 行为 |
|---|---|
| `/effort` | 展示生效值 + 来源层级（覆盖/预设/CLI 默认/内置）+ 输出上限/输入预算摘要 |
| `/effort <off\|low\|medium\|high\|xhigh\|max>` | 设置会话覆盖（写 settings；同步 L2；立即下发） |
| `/effort auto` | 清除思考长度覆盖（回退预设链） |
| `/effort out <n>` | 设置输出上限覆盖（0=清除） |
| `/effort in <n>` | 设置输入预算覆盖（0=清除） |
| `/effort think <on\|off\|auto>` | 设置 `thinking_enabled` 三态覆盖（on→true、off→false、auto→nil） |

描述串修正：`"设置思考深度 (low/medium/high)"`（现状与五档配置不一致的欠账）→ `"设置思考长度/输出上限/输入预算（off/low/medium/high/xhigh/max；out/in 设长度；think 设开关）"`。

**状态栏**：显示"思考: <档位>"（off 时追加"·关闭"）+ tokens 参数摘要（如 `out:64K/in:128K`）；数据源为 `ConfigSummary()` 扩展行。

**GUI 设置面板**：

- 四个新字段随 schema 驱动自动出现在"模型"分组（`ConfigSnapshot` 返回 schema + values；`config.set` → `ApplyConfigPatch` → `applyLiveConfig` 现有链路，保存即生效、无需重启）；`cli_defaults.effort` 枚举自动出现 off 档；
- `thinking_enabled` 的 `auto/on/off` 三态由 `applyOne` 的 `parseTriState` 转换；
- 会话级即时调整（可选增强）：复用运行时 API（`SetThinkingLength` 等）做"当前会话生成参数"面板；持久化走 settings.json；
- 校验提示（不阻断保存）：`max_output_tokens > max_context` 时警告；`thinking_enabled=off` 且 `thinking_length` 非 off 时提示"将按 off 生效"；
- 状态栏（`status.bar` 轮询）：新增"思考长度 / 输出上限 / 输入预算（含来源）"展示。

---

## 4 改造点清单

| 文件 | 动作 | 内容 |
|---|---|---|
| `internal/model/backend.go` | 修改 | 新增 `GenerationParamsBackend` 可选接口（含"为何不扩 Backend 本体"注释） |
| `internal/model/deepseek.go` | 修改 | 新字段 3 个；`maxTokens()` 新语义；`inputLimit()`；`applyGenerationParams()`（Chat/StreamEx 共用）；remap 增 off；发送前压缩改用 `inputLimit()` |
| `internal/model/budget.go` | 新增 | `EffectiveInputBudget` 纯函数（公式唯一来源） |
| `internal/model/factory.go` | 不改 | 构造签名不变（新字段经 setter 注入）——列出以示确认 |
| `internal/config/config.go` | 修改 | `ModelPreset` 4 字段；`ConfigSchema` 4+1 条；`Sanitized` 4 条；`applyOne` 4 case + `parseTriState`/`validThinkingLength`/`clampNonNeg` 辅助；`Validate` 3 条规则；`applyDefaults` 不设默认（注释说明） |
| `internal/runtime/runtime.go` | 修改 | `generationOverride` 结构；`resolve*` 四函数；`applyGenerationParams`/`ApplyGenerationParamsTo`；4 个运行时 API；`selectBackend` 应用点；`New()` 初始化顺序；`applyLiveConfig` 扩展；`ConfigSummary` 扩展 |
| `internal/runtime/settings.go` | 修改 | `saveSettings` 新键（含 effort 条件写入）；`loadSettings` 迁移规则 |
| `internal/modelgateway/handle.go` | 修改 | 缓存字段 + 6 方法；`applyParams` 透传；编译期断言；`nilBackend` 补零值实现 |
| `internal/tui/commands.go` | 修改 | `/effort` 扩展（off 档 + out/in/think 子命令 + 描述修正） |
| `internal/gui/server.go` | 修改（视实现） | 设置面板为 schema 驱动，预期零改动；实施时核对是否存在字段级白名单（`SetEffort` 调用点所在文件） |
| `config.example.yaml` | 修改 | `models` 示例新增四字段注释；`cli_defaults.effort` 注释修正（off 档 + "low→low"——现状注释"low/medium/high → 实际发送 high"已过期） |
| `contextloom/GUI实现设计文档.md` | 修改 | 设置面板"模型"分组字段清单补充四字段与三态约定 |
| `docs/contextloom改造详细设计-思考与长度释放.md` | 新增 | 本文档 |

---

## 5 分步迁移路径

每步独立可交付、可回滚（回滚 = 关闭新增代码路径 / 删除对应提交；所有新增行为默认关闭）。

| 步骤 | 交付物 | 验证 | 回滚 |
|---|---|---|---|
| 1（T1 模型层） | 可选接口 + `maxTokens()`/`inputLimit()` + 发送矩阵 + `budget.go` 纯函数 | 零值快照测试：请求体逐字不变；新增矩阵/公式单测全绿 | 删除新方法与新代码路径（未接线，无外部影响） |
| 2（T2 配置层） | 四字段 + 三处接线 + Validate | schema/sanitized/apply/validate 单测；未写配置时 Sanitized 输出与旧版一致（新增键除外） | 移除字段与接线（YAML 解析对未知键容错，旧文件不受影响） |
| 3（T3 运行时） | 解析链 + API + settings 迁移 + ctxBudget 分母 + applyLiveConfig | 零回归断言（max_tokens==maxContext）；热更新/重启恢复测试 | 恢复 `New()` 旧初始化两行；忽略 settings 新键 |
| 4（T4 网关） | handle 缓存透传 + 编译期断言 | 透传单测；网关路径参数一致性测试 | 移除 handle 新方法（类型断言路径失效但主链路不受影响） |
| 5（T5 UI） | TUI `/effort` 扩展 + GUI 字段 + 状态栏 | 命令测试 + GUI 保存即生效测试 | 保留旧 `/effort` 语法（子命令不注册即可） |
| 6（T6 收口） | 示例配置 + 设计文档 + 注释欠账修正 | 文档审查；示例可启动 | 纯文档回滚 |

**默认值切换说明**：本改造**不切换任何默认**（默认 effort 仍为 max、长度参数默认未配置、thinking 默认不发字段）。"释放"体现在上限被打开，而不是默认行为改变——这是零回归不变式的直接要求。

---

## 6 验收标准与风险清单

### 6.1 可测试断言（测试名 + 断言内容）

| # | 测试名 | 断言内容 |
|---|---|---|
| 1 | `TestGenerationParams_ZeroRegression_MaxTokensEqualsMaxContext` | 后端仅构造（无任何设置）时，Chat 与 StreamEx 请求体 `max_tokens == maxContext`，且不含 `thinking` 键、`reasoning_effort` 发送条件与现状一致 |
| 2 | `TestGenerationParams_Decoupled` | `SetMaxOutputTokens(65536)` 后请求体 `max_tokens==65536`；`SetMaxInputTokens(131072)` 后发送前压缩预算 `== EffectiveInputBudget(...)`（与 maxOutputTokens 联合收紧生效） |
| 3 | `TestThinkingMatrix_Disabled` | `sendEffort=true`、`thinkingEnabled=false`（或 `effort=off`）：请求体含 `thinking:{type:"disabled"}` 且**不含** `reasoning_effort` |
| 4 | `TestThinkingOff_ThreeLayers` | 配置 `thinking_length=off` → `Sanitized` 返回 `"off"` → 请求体同上；TUI `/effort off` → settings.json `thinking_length=="off"` → 重启后 `loadSettings` 恢复且请求体一致（UI/配置/请求体三层语义一致） |
| 5 | `TestApplyLiveConfig_GenerationParamsHotApply` | `ApplyConfigPatch({"model.max_output_tokens":65536})` 后无需重启：下一次请求体 `max_tokens==65536`；`needsRestart` 不含该键 |
| 6 | `TestSettings_PersistRestore_GenerationParams` | `SetThinkingLength("low")` + `SetMaxOutputTokens(32768)` → 用同一 settings.json 重建 Runtime → 生效值与请求体一致 |
| 7 | `TestEffectiveInputBudget_Formula` | 边界用例：未配置 / 单边配置 / 双边收紧 / 下限 10% / `maxContext<=0` |
| 8 | `TestEffortRemap_Off` | `SetReasoningEffort("off")→ReasoningEffort()=="off"`；扩展既有 `TestSetReasoningEffort_Remap`（low→low 等五档回归保留） |
| 9 | `TestGateway_GenerationParamsPassthrough` | 网关路径：`handle.SetMaxOutputTokens` 后池内实例收到（mock inner 断言）；handle 满足 `GenerationParamsBackend`（编译期断言兜底） |
| 10 | `TestConfig_Validate_ThinkingConflict` | `thinking_enabled=false` + `thinking_length="high"` → `Validate` 报错；`="off"`/`=""` → 通过 |
| 11 | `TestSettings_LegacyEffortKey` | 旧 settings.json（仅 `effort` 键）→ 加载后**不建立** thinking_length 覆盖、值写入 `cli_defaults.effort`；请求行为与旧版一致 |
| 12 | `TestMaxtokens_400Ladder_Unchanged` | 既有 `maxtokens_test.go` 全绿（400 自修复阶梯不受影响） |

### 6.2 风险清单

| 风险 | 影响 | 缓解 |
|---|---|---|
| 服务端对 `thinking` 字段的严格校验 | 个别 OpenAI 兼容端点可能 400 | ① 默认 nil 不发（零回归）；② `send_effort=false` 总开关兜底（两字段都不发）；③ 400 自修复阶梯保留；④ 文档与 UI 提示"严格端点请关闭 send_effort" |
| 思考长度与输出预算相互挤压 | 长思考 + 小 `max_output_tokens` → `finish_reason=length` 截断 | ① 既有 length 截断信号与"继续"机制（`StreamChunk.FinishReason` 注释所载行为）；② UI 建议"长思考预留余量"（思考可达数千 token）；③ 提供 off/低档降本路径 |
| 网关多实例参数一致性 | 池内轮换导致参数漂移 | handle 缓存 + `applyParams` 每次应用（既有模式）+ 编译期断言 + 透传测试（#9） |
| 旧配置 / 旧二进制兼容 | `settings.json effort=off` 被旧版透传 → 400 | `effort` 键条件写入（仅五档子集）；`loadSettings` 迁移规则；零回归断言（#1、#11） |
| ctxBudget 分母变更 | 用户可见 ctx 占比数字变化 | 仅显式配置长度时变化；`/ctx` 展示分母与来源；未配置时逐字不变（#1） |
| 预留收紧 10% 下限 | 极端组合（out=maxContext）下输入预算被压到 10% | 设置面板警告 + 文档说明；该组合本就不成立（输入无空间），下限保证仍可对话 |

---

## 7 附录

### 7.1 dsh 参照索引（包/文件/接口逐条对应）

| dsh 包/文件 | dsh 接口/常量（源码核实） | 本文对应设计 |
|---|---|---|
| `packages/llm/llm-deepseek/src/defaults.ts` | `DEFAULT_CONTEXT_WINDOW = 1_000_000`；`DEFAULT_MAX_TOKENS = 256_000` | 窗口与输出上限分离（§2.3②） |
| `packages/llm/llm-deepseek/src/config.ts` | `Config.thinking/reasoningEffort/maxTokens`（Volatile）；`deepSeekConfigFields` schema；`resolveAdapterOptions` 互斥校验与默认合并 | 配置层四字段 + `Validate` 互斥（§3.3） |
| `packages/llm/llm-deepseek/src/serialize.ts`（约 140-160 行） | effort 三级回退；`thinking:{type:'disabled'|'enabled'}`；`max_tokens` 三级回退；off 时不发 effort | 三层解析链（§2.1）+ 发送矩阵（§3.2） |
| `packages/llm/llm-deepseek/src/model-info.ts` | `REASONING_EFFORTS` 四档；`OFF_ONLY_REASONING_EFFORTS`；`defaultEffort` 推导；`defaultMaxTokens` | off 档进枚举（§2.2）+ UI 档位清单（§3.6） |
| `packages/llm/llm/src/call-config.ts` | `LlmCallConfig`；`callConfigEquals`；"logs changed snapshots"注释 | 生成参数 = 请求信封；单一热应用出口（§3.4） |
| `packages/llm/llm/src/types.ts` | `GenerateOptions.reasoningEffort/maxTokens`；`LlmResolvedModelInfo.defaultMaxTokens`；`LlmModelReasoningInfo.efforts/defaultEffort` | 模型层参数归属（§3.1/§3.2） |
| `packages/llm/llm-deepseek/src/types.ts` | `RequestDefaults.thinking/reasoningEffort`；"re-reads it per operation" | 每次操作重读配置；三态开关（§3.4） |
| `packages/llm/llm-deepseek/src/models.ts` | `DEFAULT_MODELS`（contextWindow 1M，模型级 maxTokens 可选） | 模型目录/预设语义参照（L1 层） |

**协议差异注记（如实描述）**：`thinking?: 'enabled'|'disabled'` 的定义实际位于 `llm-deepseek/src/types.ts` 的 `RequestDefaults`；`packages/llm/llm/src/types.ts` 中 "thinking" 仅出现在 `ReasoningBlock` 的注释（"Reasoning / thinking content, distinct from visible text"），`GenerateOptions` 本身没有 thinking 字段——以真实源码为准。dsh 请求体字段为 `thinking:{type}` + `output_config:{effort}`（Anthropic Messages 风格），ContextLoom 沿用 OpenAI 兼容 `reasoning_effort` 并新增 `thinking` 关闭信号字段（§1.2 注记）。

### 7.2 关键结论摘要

1. **把"窗口"这一单一旋钮拆成"窗口 / 输出 / 输入"三个独立旋钮**后，长思考与长输入不再互相踩踏——上下文工程从"一个标量"升级为"一组可组合预算"（对照 dsh `DEFAULT_CONTEXT_WINDOW` 与 `DEFAULT_MAX_TOKENS` 的分离声明）。
2. **off 档补全了思考的"零成本路径"**：格式转换、简单检索、机械编辑等任务可完全不付思考税，高强度任务仍可用 xhigh/max 满档——档位表达力从 5 档扩到 6 档（含关闭），且关闭是协议层显式声明（`thinking:{type:"disabled"}`）而非隐式省略。
3. **三层解析链 + 单一热应用出口（applyGenerationParams）**让"设置面板改一个数"与"运行时 API 改一个数"走同一条路——配置变更即请求信封变更（对齐 dsh `call-config` 的可审计性），从机制上消除"改了不生效 / 要重启"类缺陷。
4. **零回归不变式**（未配置时请求体逐字一致）把改造风险压到最低：默认行为不变，释放的是"上限"而不是"新行为"；旧配置、旧二进制、旧 mock 测试全部兼容。
5. **发送矩阵对齐 dsh 的互斥纪律**：思考关闭时不发送自相矛盾的 `reasoning_effort`；配置层拒绝矛盾组合——模型与服务端读到的永远是明确、单一意图的请求信封。这正是"上下文工程"能力释放因子中被 ContextLoom 长期混同、本次一次性打开的部分。
