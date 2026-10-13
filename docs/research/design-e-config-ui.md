# ContextLoom 改造详细设计：多轮思考触发 —— 配置、UI、兼容与实施（草案）

> 读者：ContextLoom 核心开发者（config / runtime / executor / tui / gui 维护者）。
> 目标：为"复杂问题杜绝一次思考解决、鼓励并积极触发多次思考"给出**配置面、持久化面、UI 面、兼容与实施**的接口级设计；并入主设计文档《contextloom改造详细设计-多轮思考触发.md》。
> 承接：参数面《contextloom改造详细设计-思考与长度释放.md》（已落码，本文 §0.1 逐条核对）；现状盘点《research/contextloom-thinking-inventory.md》（H1-H25）；业界对标《research/industry-multi-pass-thinking-survey.md》（§12 Top10）。
> 口径：所有"现状"以本工作区源码实测为准（文件:行号）；与设计文档不一致处如实标注（§0.1 差异清单）；目标接口为完整 Go/YAML 定义，可直接落码。
> 零回归铁律：新增机制默认关闭时，行为与当前工作区版本逐字一致。

## 0 现状核对与设计边界

### 0.1 参数面（思考与长度释放）落码现状 —— 本设计的衔接基线

| # | 机制 | 落码位置（实测） | 语义（实测） |
|---|---|---|---|
| P1 | ModelPreset 四字段 | `internal/config/config.go:237-249` | MaxOutputTokens/MaxInputTokens/ThinkingLength/ThinkingEnabled；legacyModelConfig 迁移携带（:125-128） |
| P2 | 档位清单 | `config.go:251-253` | thinkingLengthOptions = {"", off, low, medium, high, xhigh, max}；validThinkingLength 共用 |
| P3 | 五处接线 | ConfigSchema/Sanitized/applyOne/Validate/applyDefaults | 四条 schema（模型分组）；Sanitized 四键；applyOne 四 case；Validate 负值/枚举/4× 校验；applyDefaults：长度两字段缺省 0、ThinkingLength 缺省 ""，**ThinkingEnabled 缺省补 &true**（约 :1126-1140） |
| P4 | 运行时解析与热更新 | `runtime.go:1086-1126、1424-1473` | SetEffort（写 CLIDefaults + 后端 + applyGenerationParams 收敛 + saveSettings）；applyGenerationParams(b)：ThinkingEnabled 显式值优先→ThinkingLength→nil；applyLiveConfig 内 ctxBudget 用 effectiveInputBudget 重建、后端热更新后调 applyGenerationParams |
| P5 | 会话持久化 | `runtime/settings.go:24,50-52` | 仅 "effort" 键（CLIDefaults.Effort 直写）；**无 generationOverride 结构** |
| P6 | 发送矩阵 | `model/deepseek.go:961,1078-1100`；`factory.go:41` | off→thinking:disabled 且不发 effort；nil→只发 effort；true→thinking:enabled+effort；false→thinking:disabled；ApplyGenerationParams 仅下发 >0/!=nil 项 |
| P7 | 输入预算 | `deepseek.go:102,118-121,241,392` | maxTokens()/inputLimit() 已独立；发送前压缩按 inputLimit |
| P8 | UI | `tui/commands.go:140-195`（/effort、/maxlen）；`gui/server.go:743,996,1050,1111`；`app.js:4962-5143,3208-3243` | /effort 档位+长度回显、/maxlen 走 ApplyConfigPatch；GUI 设置面板 schema 驱动、model.set effort 校验、status.bar 下发 ModelInfo |
| P9 | 回显 | `runtime.go:3415` ModelInfo；`tui/widgets.go:154` | ModelInfo 暴露 maxOutputTokens/maxInputTokens/thinkingEnabled；TUI 状态栏"深度:%s" |

**与设计文档的差异（如实标注，以代码为准）**：

1. `generationOverride` 结构未落码——会话覆盖=直写 `CLIDefaults.Effort` + settings.json `effort` 键（更轻量）；
2. `model.EffectiveInputBudget` 双边收紧公式未落码——runtime `effectiveInputBudget()` 为简化版（max_input_tokens 优先，否则 max_context）；
3. `cli_defaults.effort` schema 枚举仍为五档（无 off），但 /effort 与 GUI model.set 接受 off；
4. ConfigSummary 未扩展"来源/输出/输入"展示（现状单行"思考深度: %s"）；
5. `thinking_enabled=false` 与 `thinking_length` 的互斥**硬校验未落码**（由发送矩阵软裁决）；
6. applyDefaults 把 ThinkingEnabled 缺省补 `&true`（缺省请求含 thinking:enabled）——零回归基线以**当前落码行为**为准。

### 0.2 本设计边界

**做**：多轮思考的配置项（`thinking:` 分组）、会话覆盖、TUI/GUI 接线、与参数面的衔接与冲突排查、分步实施与验收。
**不做**：改参数面既有语义；改发送矩阵；改 goalmgr/selfcheck/overseer 既有机制（仅预留挂钩）；引入多采样/ToT 搜索（业界调研建议 P2+）。

## 1 配置项设计（config.yaml schema 扩展）

**设计目标**：以"总开关 + 护栏"引入多轮思考策略；全部默认值=现状行为（policy=off 时所有新路径短路）。

### 1.1 放在哪一层：论证

- `models[]` **否决**：models 是"模型连接与生成参数"的唯一事实来源（config.go 注释明示）；思考策略是 harness 行为、跨模型一致，且切换模型不应重置策略；
- `cli_defaults` **否决**：其语义是"CLI 默认值"（model/effort/mode/threshold），会被 settings.json 会话覆盖；策略是项目级行为配置，生命周期不同；
- **结论：新增顶层 `thinking:` 分组**（与 harness/overseer/context/tools 并列）。命名空间 thinking.* 清晰、可演进；GUI Group 名"思考策略"，与"模型"分组的 model.thinking_* 明确区隔。

### 1.2 字段清单（9 项）

| YAML key | 类型 | 取值范围 | 默认值（=现状） | 非法值处理 |
|---|---|---|---|---|
| thinking.policy | string | off/auto/strict | ""→off | applyOne/Validate 拒绝枚举外 |
| thinking.min_think_rounds | int | ≥0（0=不强制） | 0 | <0 拒绝 |
| thinking.max_think_rounds | int | ≥1（0=未设置→运行时取 3） | 3（护栏） | <0 拒绝 |
| thinking.verify_pass | string | off/auto/on | ""→off | 枚举外拒绝 |
| thinking.complexity_threshold | int | 0-100 | 60 | 越界拒绝 |
| thinking.nudge_enabled | bool | true/false | false | 类型错误拒绝 |
| thinking.nudge_max | int | ≥0 | 2 | <0 拒绝 |
| thinking.round_effort_policy | string | none/escalate | ""→none | 枚举外拒绝 |
| thinking.round_effort_cap | string | low/medium/high/xhigh/max | ""→max | 枚举外拒绝 |

语义：policy=off 为总开关（其余字段不生效）；strict=复杂任务强制最少轮数并拦截收尾；auto=按 complexity_threshold 路由（软提示）；verify_pass=on 时所有回答后触发验证轮、auto 仅复杂/低质量信号；nudge=续思提示注入（结构催轮），nudge_max 为单次任务上限；round_effort=多轮时档位升级（escalate 时第 2 轮起逐级升档、封顶 cap，恢复由 runtime 收敛）。

### 1.3 五处接线草案（参照四字段既有模式）

```go
// config.go：Config 顶层新增
Thinking ThinkingConfig `yaml:"thinking"`

type ThinkingConfig struct {
	Policy              string `yaml:"policy"`               // ""|off|auto|strict
	MinThinkRounds      int    `yaml:"min_think_rounds"`     // ≥0；strict 硬下限/auto 软下限
	MaxThinkRounds      int    `yaml:"max_think_rounds"`     // ≥1；多轮机制额外轮数护栏（0=取默认3）
	VerifyPass          string `yaml:"verify_pass"`          // ""|off|auto|on
	ComplexityThreshold int    `yaml:"complexity_threshold"` // 0-100
	NudgeEnabled        bool   `yaml:"nudge_enabled"`
	NudgeMax            int    `yaml:"nudge_max"`            // ≥0
	RoundEffortPolicy   string `yaml:"round_effort_policy"`  // ""|none|escalate
	RoundEffortCap      string `yaml:"round_effort_cap"`     // ""|low|medium|high|xhigh|max
}

// 选项清单（ConfigSchema/Sanitized/applyOne/Validate 共用，杜绝多处漂移）
var (
	thinkingPolicyOptions    = []string{"off", "auto", "strict"}
	verifyPassOptions        = []string{"off", "auto", "on"}
	roundEffortPolicyOptions = []string{"none", "escalate"}
	roundEffortCapOptions    = []string{"low", "medium", "high", "xhigh", "max"}
)
```

**① ConfigSchema（Group "思考策略"，9 条）**——示例（其余同构）：

```go
{Key: "thinking.policy", Label: "思考策略", Type: "select", Group: "思考策略", Default: "off",
 Enums: thinkingPolicyOptions, Desc: "off=不干预（现状）；auto=复杂任务触发多轮；strict=复杂任务强制多轮并拦截收尾"},
{Key: "thinking.min_think_rounds", Label: "最少思考轮数", Type: "number", Group: "思考策略", Default: 0,
 Min: f64p(0), Max: f64p(20), Step: 1, Desc: "复杂任务最少思考轮数（0=不强制）"},
{Key: "thinking.max_think_rounds", Label: "思考轮数上限", Type: "number", Group: "思考策略", Default: 3,
 Min: f64p(1), Max: f64p(20), Step: 1, Desc: "多轮思考额外轮数护栏（防过度思考；工具执行轮次不受限）"},
{Key: "thinking.verify_pass", Label: "验证轮", Type: "select", Group: "思考策略", Default: "off",
 Enums: verifyPassOptions, Desc: "off=无（现状）；auto=复杂/低质量信号触发；on=回答后必验"},
{Key: "thinking.complexity_threshold", Label: "复杂度阈值", Type: "number", Group: "思考策略", Default: 60,
 Min: f64p(0), Max: f64p(100), Step: 5, Desc: "评估分≥阈值视为复杂任务"},
{Key: "thinking.nudge_enabled", Label: "续思提示 (nudge)", Type: "bool", Group: "思考策略", Default: false,
 Desc: "收尾前注入结构续思提示"},
{Key: "thinking.nudge_max", Label: "续思次数上限", Type: "number", Group: "思考策略", Default: 2,
 Min: f64p(0), Max: f64p(10), Step: 1, Desc: "单次任务 nudge 注入上限"},
{Key: "thinking.round_effort_policy", Label: "轮次思考强度", Type: "select", Group: "思考策略", Default: "none",
 Enums: roundEffortPolicyOptions, Desc: "none=保持（现状）；escalate=多轮逐级升档"},
{Key: "thinking.round_effort_cap", Label: "升档上限", Type: "select", Group: "思考策略", Default: "max",
 Enums: roundEffortCapOptions, Desc: "仅 escalate 生效；恢复由运行时收敛"},
```

**② Sanitized**：`m["thinking.policy"] = normalize(c.Thinking.Policy)`（""→"off"）等 9 键；数值 0 原样输出（语义=未设置）。
**③ applyOne**：9 case；枚举走 `inList(val, opts)` 校验后 `setStr`；数值 `setInt`；布尔 `setBool`（沿用既有 helper 风格）。
**④ Validate**：枚举校验 + 范围校验 + 交叉校验 `min_think_rounds>0 && max_think_rounds>0 && min>max → 报错`；policy=off 时其余字段**不做硬拒绝**（软裁决：运行时 policy 优先，GUI 提示"不生效"）——对齐参数面落码现状（互斥软裁决）。
**⑤ applyDefaults**：**不写任何值**（零值语义：""/"none"/0/false=现状）；仅加注释说明零值含义，运行时解析函数负责归一（""→off 等）。

**挂钩点**：internal/config/config.go（struct/ConfigSchema/Sanitized/applyOne/Validate/applyDefaults）；config.example.yaml（thinking: 示例段）。
**测试断言**：`TestThinkingConfig_FiveWiring`（schema 9 条/sanitized 9 键/applyPatch 往返/validate 拒绝表/defaults 不改值）；`TestThinkingConfig_DefaultsEqualCurrent`（Load 空配置后 Sanitized 与现状一致）。
**风险**：新分组与参数面 model.thinking_* 命名混淆——通过 Group 名与文档区分；用户误设 policy=off 却调 verify——回显提示兜底。

## 2 settings.json 持久化与会话覆盖

**设计目标**：为"最可能临时切换"的两把开关（policy/verify_pass）提供会话级覆盖；其余数值参数保持配置级；旧 settings 零迁移。

### 2.1 覆盖范围

- **L3 会话覆盖**（settings.json + /think 即时改）：`thinking.policy`、`thinking.verify_pass`；
- **L1 配置级**（config.yaml，GUI 面板/手工编辑）：min/max_think_rounds、complexity_threshold、nudge_enabled、nudge_max、round_effort_*；
- 论证：开关型字段是"实验/临时提级"的交互对象，与参数面 effort 同级；数值型 tuning 参数写 settings 会导致跨会话语义混乱与键膨胀。

### 2.2 复用 generationOverride 还是新建？

- 事实：`generationOverride` **未落码**（§0.1 差异 1）——参数面以"直写 CLIDefaults.Effort + settings `effort` 键"实现；不存在可复用结构。
- **结论：新建独立 `thinkOverride` 结构**。论证：① 语义不同（生成参数=请求信封；思考策略=任务轮次行为），生命周期不同；② 需区分"未覆盖 vs 显式值"（*string 指针）；③ 不动既有 `effort` 键，零回归；④ 若未来参数面引入统一会话覆盖结构，二者可合并为 `sessionOverrides`（预留演进，本期不合并）。

### 2.3 结构草案与迁移规则

```go
// internal/runtime/thinking.go（新增）
// thinkOverride 会话级多轮思考策略覆盖（nil=未覆盖；settings.json 持久化）。
type thinkOverride struct {
	policy     *string // off/auto/strict
	verifyPass *string // off/auto/on
}
// Runtime 新字段：thinkOverride thinkOverride
// 新增方法：SetThinkingPolicy(p string) error / SetVerifyPass(v string) error /
//           ThinkingStatus() ThinkStatus（TUI/GUI 状态数据源）
```

| settings.json 键 | 语义 | 写入者 | loadSettings 规则 |
|---|---|---|---|
| thinking_session.policy | 策略覆盖 | /think、SetThinkingPolicy | 键存在且值合法→建立覆盖；非法值忽略（不报错） |
| thinking_session.verify_pass | 验证轮覆盖 | /think、SetVerifyPass | 同上 |
| （旧文件）无 thinking_session | 无覆盖 | — | 不建立覆盖→回落配置值→零回归 |

- **兼容性**：loadSettings 为逐键 type-assert 读取（settings.go:44-100 实测），未知键天然忽略——**新文件被旧二进制读取安全**；旧文件被新版读取安全（无键=无覆盖）。
- **清除规则**：`/think policy clear` → 置 nil 并删键；GUI 保存 config 级 thinking.*（ApplyConfigPatch）→ 清除对应覆盖（"保存新默认=新意图"，对齐参数面规则）。
- **失效值处理**：覆盖值读取时仍过枚举校验（防手改 settings 注入非法值）。

**挂钩点**：internal/runtime/settings.go（save/load 新键）；runtime.go（thinkOverride 字段、三个新方法）。
**测试断言**：`TestThinkingSession_PersistRestore`（设覆盖→重建 Runtime→生效值一致）；`TestThinkingSession_LegacySettingsCompat`（旧 settings 无键→无覆盖）；`TestThinkingSession_InvalidValueIgnored`。
**风险**：会话覆盖与 config 面板"双来源"困惑——状态栏/设置面板均展示来源（覆盖/配置）。

## 3 TUI 接线设计

**设计目标**：/think 命令族与状态栏轮次展示；与既有 /effort（档位）、/maxlen（长度）正交可组合、零冲突。

### 3.1 命令设计（internal/tui/commands.go）

| 命令 | 行为 |
|---|---|
| /think | 展示：策略（含来源）/最少轮数/上限/验证轮/nudge/升档策略 + 当前轮次进度 |
| /think policy <off\|auto\|strict\|clear> | 设置/清除会话策略覆盖 |
| /think verify <off\|auto\|on\|clear> | 设置/清除验证轮覆盖 |
| /think rounds <n> | 设置 min_think_rounds（配置级，走 ApplyConfigPatch） |
| /think nudge <on\|off> | 设置 nudge_enabled（配置级） |
| /think effort <none\|escalate> | 设置 round_effort_policy（配置级） |

- 与 /effort 关系：`/effort` 管"每轮思考多长"（参数面，语义不变）；`/think` 管"思考几轮/是否验证"（轮次面）。组合示例：`/effort max` + `/think policy strict` = 每轮满档 + 强制多轮；`/effort off` + `/think policy strict` = 矛盾组合，运行时以 effort=off 优先（不发思考字段），/think 回显提示"当前档位 off，思考不会实际发生"。
- 配置级子命令走 ApplyConfigPatch（持久化+热更新，与 /maxlen 同路径）；会话级走 SetThinkingPolicy/SetVerifyPass（settings.json）。

### 3.2 状态栏（internal/tui/widgets.go:154）

- 现状：`模型:%s · %s · 深度:%s · 阈值:%.0f`；
- 扩展：policy≠off 时追加 ` · 思考:%s·轮%d/%d`（如 `思考:strict·轮2/3`）；验证轮进行中追加 `·验证中`；
- 数据源：`rt.ThinkingStatus()`；off 时**不显示**（零回归）；
- 刷新点：tui.go:1871 附近（既有 effort 刷新处）并列刷新。

**挂钩点**：commands.go（注册 /think、descs）；widgets.go:154；tui.go:1871。
**测试断言**：`TestThinkCmd_SetAndClear`（各子命令回显/持久化）；`TestThinkCmd_ConflictHint`（/effort off + /think strict 提示）；`TestStatusBar_ThinkHiddenWhenOff`（零回归）+ `TestStatusBar_ThinkShown`。
**风险**：状态栏长度溢出（窄终端截断策略：轮次信息优先于阈值展示）。

## 4 GUI 接线设计

**设计目标**：设置面板零前端改动出现新字段；状态栏展示轮次/验证状态；会话级即时调整（可选）。

### 4.1 设置面板：schema 驱动零改动论证

- 实测：app.js:4962-5143 由 `config.get` 返回的 schema 遍历渲染（`for (const f of state.cfgSchema)`），保存时 diff → `config.set`；后端 `ConfigSchema()` 增 9 条 + `Sanitized()` 增 9 键 + `applyOne` 增 9 case 后，**前端零改动**自动出现"思考策略"分组（按 Group 字段分组渲染）。
- 结论：预期零前端改动；实施时核对点=app.js 是否存在字段级白名单/特殊控件分支（当前未发现）。

### 4.2 会话级即时调整面板（可选，P2）

- 新增 RPC `thinking.set`：`{policy?, verify_pass?}` → SetThinkingPolicy/SetVerifyPass → 返回 `{ok, status}`；前端在模型面板加两个下拉（复用 model.set 交互模式，server.go:1050 旁）。
- 持久化：settings.json（会话级）；不写 config.yaml。

### 4.3 状态展示

- `status.bar`（server.go:743-748）从 ModelInfo 取；ModelInfo（runtime.go:3415）扩展 `thinking: {policy, round, maxRound, verify, verifying, source}`；
- app.js `modelInfoSuffix`（3208-3210）追加 ` · 思考:strict 轮2/3`（policy≠off 时）；格式与 TUI 对齐。

**挂钩点**：internal/gui/server.go（status.bar/model.info/可选 thinking.set）；cmd/contextloom-gui/web/app.js（modelInfoSuffix；schema 表单自动）。
**测试断言**：`TestGUISchema_ContainsThinkingGroup`（ConfigSnapshot schema 9 条+group）；`TestGUIThinkingSet_Roundtrip`（可选）；`TestModelInfo_ThinkStatusExposed`。
**风险**：GUI 轮询频率下轮次跳变（用"当前任务快照"语义，非实时流式）。

## 5 与"思考与长度释放"设计的衔接与依赖顺序

### 5.1 共用挂钩点

H10（deepseek.go:1078 applyThinkingParams，per-request 注入——轮次级 effort 覆盖的模型层落点）、H11（deepseek.go:961 SetReasoningEffort，动态切档 API）、H12（runtime.go:1108 applyGenerationParams，统一热应用出口）、H23（settings.go:24 effort 持久化，会话覆盖模式参照）。四者参数面均已落码，轮次面**只消费不修改**其语义。

### 5.2 依赖顺序

参数面（已完成）→ 轮次面（本设计）。轮次面依赖：① off 档存在（验证轮可降档收尾/省成本）；② SetReasoningEffort 动态切档；③ "档位先于开关"调用铁律（轮次升档后必须走 applyGenerationParams 收敛，防 thinkingEnabled 被抹）。

### 5.3 冲突排查（逐条）

| # | 潜在冲突 | 结论 | 处置 |
|---|---|---|---|
| C1 | 键名 thinking.*（新分组） vs model.thinking_length/thinking_enabled | 不同命名空间 | 文档/GUI 分组名区分 |
| C2 | settings.json 新键 vs effort 键 | 独立键 | 互不覆盖 |
| C3 | /think vs /effort /maxlen | 正交 | 独立注册；/think 不劫持参数面语法 |
| C4 | round_effort 切档 vs 参数面 thinkingEnabled 复位 | 存在风险 | 升档仅向上（不发 off）；恢复走 applyGenerationParams 收敛（铁律） |
| C5 | 拦截收尾 vs detectLoop/noToolRounds 护栏 | 存在交互 | max_think_rounds 护栏 + 拦截只在"非空正文且无工具调用"分支（H1/H2）；不碰既有护栏 |
| C6 | 默认值 | 全部=现状 | policy off 短路所有新路径 |

### 5.4 零回归保证

新增机制全部默认关闭（policy=off / verify=off / nudge=false / escalate=none）时：请求体、settings.json、状态栏、设置面板与当前版本逐字一致（基线=当前工作区落码行为，含 §0.1 差异 6 的 thinking 缺省语义）。

## 6 分步实施路径与验收标准

### 6.1 分步清单（每步独立可交付、可回滚）

| 步骤 | 交付物 | 验证方法 | 回滚方式 |
|---|---|---|---|
| S1 评估器 | internal/thinking/score.go（纯函数 Score）；config thinking: 分组五处接线 | 表驱动单测（阈值路由）；`go build ./... && go test ./internal/thinking/ ./internal/config/` | 删包/删字段（无调用方） |
| S2 收尾拦截 | executor 注入面（SetThinkPolicy）+ H1/H2 拦截（strict/auto）+ max 护栏 | agent 流测试：拦截注入文本/轮数上限/零回归快照 | policy 短路（注入 nil） |
| S3 验证轮 | CoVe 形态注入（回答后独立复核问题）+ 状态标记 | 注入文本断言；轮次计数 | verify=off 短路 |
| S4 nudge | thinkNudge（复制 batchNudge 模式 agent.go:101-116）+ nudge_max 频控 | 频控单测；注入次数断言 | nudge_enabled=false 短路 |
| S5 effort | 轮次级升档（H10-H12）+ 恢复收敛 | 升档/封顶/恢复不泄漏单测 | escalate=none 短路 |
| S6 度量与 UI | trace think_round 事件（H21）；TUI/GUI 状态展示 | 事件断言；状态栏测试 | 纯展示回滚 |

### 6.2 验收测试断言清单（14 条）

1. `TestThinkingConfig_FiveWiring`：schema 9 条（group=思考策略）/Sanitized 9 键/applyOne 往返/Validate 拒绝表/defaults 零值；
2. `TestThinking_ZeroRegression_OffShortCircuit`：默认配置下 executor 不注入任何新提示、请求体与当前版本一致；
3. `TestThinkingScore_TableDriven`：复杂度评估表驱动（长度/模糊度/多约束信号），阈值 60 路由正确；
4. `TestStrictPolicy_MinRoundsBlocksFinish`：strict+min=2 时首轮非空正文被拦截并注入续思，第 2 轮后放行；
5. `TestAutoPolicy_ComplexityRouting`：复杂输入触发、简单输入不触发（现状路径）；
6. `TestVerifyPass_AutoAfterAnswer`：verify=auto+复杂→回答后注入独立复核问题；verify=off→不注入；
7. `TestThinkNudge_FrequencyCap`：nudge_max=2 时第 3 次不再注入；
8. `TestMaxThinkRounds_Guard`：达到上限后强制放行收尾（不产生死循环）；
9. `TestRoundEffort_EscalationAndRestore`：escalate 下第 2 轮升档、封顶 cap、任务结束恢复原档（走 applyGenerationParams 收敛）；
10. `TestThinkingSession_PersistRestore`：policy/verify 覆盖→settings.json→重建 Runtime 恢复；
11. `TestThinkingSession_LegacySettingsCompat`：旧 settings（无 thinking_session）→无覆盖、行为不变；
12. `TestGUISchema_ThinkingGroupAutoAppears`：ConfigSnapshot schema 含 9 条思考策略字段（前端零改动前提）；
13. `TestThinkStatus_ExposedToUI`：ThinkingStatus/ModelInfo 暴露 policy/round/maxRound/verify/source；
14. `TestThinking_NoConflict_EffortCombo`：/effort off + policy=strict 组合：effort 优先关闭思考，状态提示矛盾。

### 6.3 风险清单

| 类别 | 风险 | 缓解 |
|---|---|---|
| 技术 | 拦截与既有护栏（detectLoop/noToolRounds/forcedFinal）交互产生死循环 | max_think_rounds 硬护栏；拦截仅限收尾分支；复用现有"注入上限"模式（batchNudge/brokenToolRounds 先例） |
| 技术 | 轮次级档位泄漏到后续任务 | 恢复路径统一走 applyGenerationParams 收敛；单测锁定 |
| 技术 | executor 注入面（nil 安全）破坏现有测试 mock | 可选注入（SetThinkPolicy(nil) 默认）；不改既有接口本体 |
| UX | 强制多轮拖慢简单任务 | 默认 off；auto 按阈值路由；简单输入走现状单轮 |
| UX | 状态栏信息过载 | policy=off 不显示；窄终端截断优先级 |
| 成本 | 多轮思考 token 成本上升（按输出计费） | 护栏+阈值+off 降本路径；文档明示成本语义 |
| 兼容 | 旧二进制读新 settings / 旧 settings 被新版读 | 逐键读取天然忽略未知键；无键=无覆盖（实测验证） |

## 7 附录：章节 ↔ 挂钩点 ↔ 测试对应表

| 章节 | 挂钩点（文件:行号） | 对应测试文件（规划） |
|---|---|---|
| §1 配置 | config.go（struct/ConfigSchema/Sanitized/applyOne/Validate/applyDefaults）；config.example.yaml | internal/config/thinking_config_test.go |
| §2 会话 | settings.go:24,50-52；runtime.go（thinkOverride） | internal/runtime/thinking_session_test.go |
| §3 TUI | commands.go:140-195；widgets.go:154；tui.go:1871 | internal/tui/think_cmd_test.go |
| §4 GUI | server.go:743,996,1050；app.js:3208-3243,4962-5143 | internal/gui/thinking_panel_test.go |
| §5 衔接 | deepseek.go:961,1078；runtime.go:1108；settings.go:24 | internal/runtime/generation_params_test.go（回归） |
| §6 实施 | executor/agent.go:101-116,1226-1286（盘点口径 :416-419）；trace/trace.go:25 | internal/executor/think_round_test.go |

---

*草案完。本文与《contextloom改造详细设计-多轮思考触发.md》主文档合并时，§0.1 差异清单为必读前置；所有"实测"引用均来自本工作区代码核对（截至本草案生成时）。*
