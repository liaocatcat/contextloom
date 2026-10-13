<div align="center">

# ContextLoom

**上下文感知的编码智能体 Harness——为你的 LLM 编织恰到好处的上下文，终端与桌面双形态。**

[English](README.md) | [中文](README.zh-CN.md)

Go 1.22+ · macOS / Linux / Windows · MIT 许可证

</div>

---

ContextLoom 是一个用 Go 编写的编码智能体 Harness（驾驶舱）。它通过终端（TUI）、原生桌面窗口（GUI）或任何兼容 MCP/ACP 的外部工具驱动 LLM，同时由上下文引擎保证每次请求的信息密度：长对话按 token 预算渐进压缩、被压缩的原文永不丢失（可检索找回）、项目记忆跨会话持久、子智能体的产出要经过评分门禁审查后才会呈现给你。

单二进制、零外部依赖（无数据库、无向量库、无消息队列），所有状态以 JSON/JSONL 落盘在项目内，随仓库自包含、可共享。

## 为什么是 ContextLoom：与"裸跑 Agent"的本质差异

通用 Agent 框架常见的问题，ContextLoom 都有对应的机制性回答：

| 常见痛点 | ContextLoom 的回答 |
|---|---|
| 长对话超窗后"断崖失忆" | token 预算驱动的**渐进式分层压缩**（前缀截断 → 丢弃最旧 → 滚动摘要），且**被压缩原文 100% 可通过 `harness_search` 找回**——压缩 ≠ 丢失 |
| 检索靠外挂向量库、重资产 | 内置 **Harness 混合检索引擎**：倒排索引 + 符号索引 + 轻量语义重排（中英跨语言同义词、FNV 哈希嵌入），零依赖、纯内存、可持久化 |
| 子 Agent 产出质量不可控、"声称完成"无法验证 | **监工（Overseer）+ 质量门禁**：证据链核验、测试透明代理（TEE）实测对比防自我欺骗，不达标打回重做、绝不外显 |
| Agent 会话崩溃后进度全丢 | **多 Agent 检查点**（对标 LangGraph checkpointer 的 JSONL 落地），goal 循环每轮自动落盘、崩溃恢复续跑 |
| 提示词工程靠手工、越干越笨 | **员工经验自动蒸馏**：子任务完成后模型自动提炼"金规/踩坑/风格"沉淀进员工本体档案，下次指派自动注入——员工越干越熟 |
| 模型路由、限流、故障切换各写各的 | **模型网关**：按 role/task/budget 规则路由、多 Key 池化轮换、熔断自动降级 |
| 工具执行无法回滚、有安全风险 | **工具沙箱**：写操作重定向影子文件系统、网络强制隔离（代理指向死端口）、commit/discard 两段式提交 |

## 核心特性

### 1. 上下文精细管理（ctxmgr）

上下文管理是 ContextLoom 的第一公民，核心是 `TokenBudget` 预算控制器——所有上下文策略（Runtime / Agent / Backend）的唯一决策者：

- **三档渐进阈值**（可配置，自动保证单调）：
  - `compact_ratio: 0.70` —— 用量达 70% 开始前缀截断旧消息；
  - `hard_ratio: 0.85` —— 达 85% 丢弃最旧消息并累积滚动摘要；
  - `safety_ratio: 0.92` —— 发送前兜底红线，超限自动压缩/强制收尾；
  - `keep_recent: 8` —— 最近 N 条完整消息永不丢弃。
- **滚动摘要**：被丢弃消息的要点累积注入为 system 消息，避免断崖失忆。
- **压缩审计**：每次压缩动作（时间+影响范围）记录在案，`/ctx` 命令可查"到底压缩了什么"——压缩过程可审计、可解释。
- **压缩原文可找回**：被截断/丢弃的原文通过回调写入 Harness 检索索引并持久化到 `.contextloom/harness/index.json`，进程重启后仍可通过 `harness_search` 检索找回（含 `[压缩原文]` 标记）。
- **多模态预算**：图片输入纳入 token 预算管理，后端不支持识图时自动降级为路径文本提示。
- **思考消息保护**：reasoning/thinking 消息在压缩中按"保留最近"策略单独处理，不污染历史。

### 2. harness_search：被压缩对话的检索找回（本项目特有能力）

`harness_search` 是注册到本机 Agent 工具表的检索类工具（同时经 MCP Server 只读暴露），专门解决"上下文被压缩后信息去哪了"：

- **数据源**：压缩冲刷的对话原文、项目上下文块；搜索前自动冲刷收集区，保证"压缩过就能搜到"。
- **混合检索引擎**（`internal/harness`）：
  - **倒排索引**：关键词全文检索；
  - **符号索引**：正则提取 Go/Python/JS/TS/Rust 的函数/类型/常量/类名建立符号 → 块映射，查询命中符号时加权（对标 repo map 的符号感知，零依赖替代 tree-sitter）；
  - **语义重排**：查询覆盖率 + 特征哈希嵌入余弦（FNV-1a hashing trick）0.5/0.5 融合；内置中英编码术语同义词表（"登录"↔login、"超时"↔timeout……），中文提问也能命中英文代码块；
  - **时间衰减**：未被引用的块按 `decay_factor` 衰减，主动压缩回收（`/compress`）；
  - **滑动窗口锚定区**：重要块可手动提升（`/inject <id>`）常驻上下文。
- **会话隔离**：检索结果只含"项目级共享块 + 当前会话私有块"，同项目多会话（task/goal 并发）绝不串扰——串扰根因有回归测试看守。
- **结果缓存**：LRU + TTL，缓存键含会话 ID，返回深拷贝防污染。
- **持久化**：索引变更按 changeSeq 记账，落盘 `.contextloom/harness/index.json`，进程重启后检索能力不丢。

### 3. 三种工作模式（`/mode`）

| 模式 | 行为 |
|---|---|
| `chat` | 单对话，自然语言交互（默认） |
| `task` | 多任务并发：JSON 极简提示词、子 Agent 风扇出、一次执行即结束 |
| `goal` | 目标持久循环：规划 → 执行 → 检查 → 下一轮，自动续跑直到 `[GOAL_DONE]` / token 预算 / 手动中止 |

**goal 循环细节**：状态机按对话会话隔离（多会话各持独立目标）；每轮生成 continuation（上轮总结+下一步指令）注入下轮；每轮结束写检查点，崩溃/中断后 `/goal resume <id>` 恢复续跑；连续无进展轮次触发告警并强制换策略（拆分并行 / 检索历史 / 请求澄清）；步骤证据链核验——done 步骤的 proofs 未通过自动降级 pending 并追加缺陷。

### 4. 监工与质量门禁（Overseer + Gates）

- **监工评分**：每份子智能体产出按可配置阈值（默认 90/100）打分，低于阈值打回并附批判意见，绝不外显。
- **反作弊三件套**：
  - **测试透明代理（TEE）**：声称"测试通过"时沙箱内实测对比，声称与实际的通过/失败数相似度 < 0.8 即判不可信；
  - **证据链核验**：产出必须引用物理证据（文件路径、测试运行结果），口头声明无效；
  - **静态核查**：目标循环每轮结束强制静态核查 + TEE 实测，不过不外显（禁写 `[GOAL_DONE]`，缺陷注入下一轮修复清单）。
- **质量三档**（`gate.yaml: level`）：`standard`（全开）/ `fast`（仅静检）/ `none`（关闭）。
- **质量账本**：每轮 Fail 裁决把缺陷签名写入 `quality_ledger.yaml`，持久怀疑记录、重启不丢。

### 5. 员工体系与经验自动蒸馏（Employees）

- 员工 = 角色预设档案（YAML）：persona、工具白/黑名单、模型绑定、记忆切片、审批策略、技能。内置六员工：`coder / tester / reviewer / product / writer / translator`。
- `spawn_task(directive=<员工名>)` 即按档案装配子 Agent；TUI 中 `/emp` 管理与指派。
- **自选模型与多来源搭配组合**：员工档案 `model.preset` 直接绑定模型预设（无需网关规则），`model.fallbacks` 声明跨来源降级链（可本地+远程混搭）；`spawn_task(model=<预设名>)` 还可让模型按任务临时自选。优先级：spawn_task model 参数 > 员工 model.preset+fallbacks（组合为链式后端，主来源限流/熔断自动切备选）> 网关规则 > 默认模型。
- **经验自动蒸馏**（本项目特色）：子任务完成并命中员工时，模型自动把本次产出提炼为经验条目（金规/踩坑/风格），去重追加进该员工的 `ONTOLOGY.md`；下次指派自动注入上下文——**员工越干越熟，蒸馏由 Agent 自动完成而非用户功课**。非阻塞、幂等（哈希去重）、容量受限（单次 ≤1KB）、失败静默降级。

### 6. 一切皆插件：场景 / 工具链 / 门禁 / Manifest 插件

- **场景（scenes）**：关键词命中自动接管对话（如"帮我写周报"→ weekly 场景），捆绑团队编排/模型/工具链/门禁；未命中则与裸 chat 完全一致。
- **工具链（chains）**：声明式 DAG 流水线——`tool`（工具步骤）/ `llm`（模型步骤）/ `ref`（产物引用）三类节点，`depends_on` 依赖编排、`$ref` 产物协议、`{{param.*}}` 参数注入、失败重试。内置：`bug-fix-chain`、`code-review-chain`、`code-test-review-chain`、`pr-review-chain`、`weekly-report-chain`。
- **门禁（gates）**：`.contextloom/gates/` 目录存在即启用。
- **Manifest 插件**：`stages`（pre/during/post）+ `hooks`（message/route）+ 自定义脚本工具（含超时与沙箱）。示例见 [`examples/plugins/`](examples/plugins/)——如 `minimal-anchor`（首轮极简锚定 → 次轮释放的提示词工程插件）。
- 项目级（`.contextloom/`）与用户级（`~/.contextloom/plugins/`）自动合并，同名项目级优先。

### 7. 多 Provider 模型网关

- 任意 [OI]（OpenAI 兼容）端点：llama.cpp、ollama、vLLM、本地、云端；可选 `deepseek` 官方通道（额外发送 `reasoning_effort`）。
- **规则路由**：`role=reviewer → strong-model`、`task=vision → vision-model`、`budget<=low → cheap-model`，逗号分隔谓词取交集。
- **多 Key 池化**：轮换、每池并发上限、指数退避重试。
- **熔断降级**：连续失败触发熔断自动切池，提示可见。
- 模型预设热切换（`/model <name>`），`vision_model` 识图自动切换，`supports_vision` 不支持时降级。

### 8. 持久记忆（Memory）

- 会话 = 追加式 JSONL 事件日志（`sessions/<uuid>.jsonl`）+ 元数据 JSON；`--memory-load` / `/session` 恢复任意会话（含压缩快照基线 + 增量）。
- **长期教训**（`lessons.md`）：`/teach` 手动沉淀，`/dream` 自动整理——扫描历史会话抽取教训/经验，幂等去重。
- 无全局锁文件：同项目多实例安全。

### 9. 工具沙箱（Sandbox）

- **影子文件系统**：所有写操作重定向到 `.contextloom/sandbox/`（读镜像优先、列表合并）；`/sandbox commit` 两段式合并写回，`/sandbox discard` 丢弃。
- **网络强制隔离**：沙箱会话内命令的 HTTP(S)_PROXY/ALL_PROXY 强制指向死端口（连接立即失败），删除 NO_PROXY 防绕过——对标容器 `--network=none` 的零依赖落地。
- 资源限制：单次运行内存限额、CPU 限制、exec 超时。
- 审批预设：`standard`（敏感操作确认）/ `permissive`（全自动）/ `strict`（全部确认）；task/goal 自动化模式自动批准。
- 文件操作审计：JSONL 按日轮转（`internal/audit`），企业治理审计追踪。

### 10. 可观测性与评估

- **JSONL trace**：按日轮转 `.contextloom/trace/`，记录每轮迭代/工具调用/耗时/错误；TUI `/trace` 或 CLI `trace --tail N` 查看。
- **OTLP 导出**：零依赖手写 OpenTelemetry LogsData 协议，可接入 Collector / Langfuse / Jaeger。
- **离线评估**：`contextloom eval`（agent 级，`--dry-run`/`--real`）、`contextloom eval-retrieval`（检索质量，Recall@K / MRR）。

### 11. 生态接入

- **MCP 双向**：MCP Client 消费外部工具（动态注册为 `mcp.<server>.<tool>`）；MCP Server 以 stdio JSON-RPC 只读暴露 Harness 检索/记忆/技能，供 [CC]、Cursor 等驱动。
- **ACP 服务**：`--serve` headless 模式（GUI 启动器即拉起它）。
- **外部触发渠道**：webhook（HTTP POST + token 鉴权，随机端口）、cron（5 字段表达式定时触发 goal/task/chat，精度 30s 内）。
- **技能市场**：`/market` 搜索内置技能、`/market install <name>` 安装，`/skills` 交互式多选启用。

## 全平台构建指南（TUI / GUI 全命令矩阵）

### TUI 核心（纯 Go，任何平台零 CGO）

TUI 是纯 Go 实现，交叉编译天然支持，`CGO_ENABLED=0` 即可全平台产出：

```bash
# macOS (Apple Silicon / Intel)
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o dist/contextloom-darwin-arm64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-darwin-amd64 ./cmd/contextloom

# Linux (amd64 / arm64 / 386 / arm)
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-linux-amd64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o dist/contextloom-linux-arm64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=386   go build -ldflags="-s -w" -o dist/contextloom-linux-386 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm   GOARM=7 go build -ldflags="-s -w" -o dist/contextloom-linux-armv7 ./cmd/contextloom

# Windows (amd64 / arm64)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-windows-amd64.exe ./cmd/contextloom
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o dist/contextloom-windows-arm64.exe ./cmd/contextloom

# FreeBSD
CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -ldflags="-s -w" -o dist/contextloom-freebsd-amd64 ./cmd/contextloom
```

> ⚠️ TUI 的 Windows 构建**不要**加 `-H windowsgui`：TUI 是控制台程序，加了会导致无法在终端交互输出。
>
> 💡 Windows 原生用户注意：bash 的 `VAR=x cmd` 变量前缀语法在 PowerShell/CMD 中不可用，请改用 `$env:GOOS="windows"` 等 `$env:` 赋值写法（含完整 PowerShell/CMD 等价命令与 CMD `set` 的尾随空格坑说明），见 [docs/build.zh-CN.md §2.1](docs/build.zh-CN.md)。

或使用 Makefile（封装同样命令）：

```bash
make build     # 当前平台 TUI 二进制
make test      # -race + 覆盖率
make lint      # golangci-lint
```

### GUI 桌面外壳（Wails/WebView，需 CGO）

GUI 外壳会从自身所在目录拉起 `contextloom` 后端（或用 `CONTEXTLOOM_BIN` 环境变量指定路径），**请把两个二进制放在同一目录**。

#### macOS（本机构建）

```bash
# GUI 二进制（当前平台）
# macOS 15+ 会打印 Wails 的 setShowsBaselineSeparator: 弃用告警（无害，非错误）；
# 加 CGO_CFLAGS 可屏蔽该告警：
CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" \
  go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui

# 打包 .app（Finder 双击以 GUI 应用启动，避免 LaunchServices 用 Terminal 打开裸二进制）
make build-gui-macos        # 内部执行 scripts/build-macos-app.sh
```

> ⚠️ **macOS 15+ 的 `setShowsBaselineSeparator:` 弃用告警无害，别当报错。** 这是第三方依赖 Wails v2 调用已弃用 AppKit API 的**警告（warning）**，构建仍成功并产出可用二进制；所有 Wails v2 版本（含 v2.16.0）均如此，升级无法消除。如需日志清爽，加一行 `CGO_CFLAGS="-Wno-deprecated-declarations"` 重新构建即可（已验证构建仍成功、告警消失）。

#### Windows（本机或交叉编译均需 mingw-w64）

```bash
# 本机（Windows 上装了 mingw-w64 或 MSYS2）
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui

# 从 macOS/Linux 交叉编译 Windows GUI
# 前置：apt/brew 安装 mingw-w64（提供 x86_64-w64-mingw32-gcc）
CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui

# Windows on ARM
CGO_ENABLED=1 CC=aarch64-w64-mingw32-gcc GOOS=windows GOARCH=arm64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o contextloom-gui-windows-arm64.exe ./cmd/contextloom-gui
```

> ⚠️ **`-H windowsgui` 是生死标志**：Windows GUI 构建漏掉它会产出 console 子系统 exe（Subsystem=3），双击必弹黑色命令行窗口。该标志按**目标 GOOS=windows** 生效，与构建主机无关——在 Linux/macOS 上交叉编译时同样必须带。反过来说，在 macOS/Linux 宿主上构建 GUI **不要**加它（只对 `GOOS=windows` 有效，加到 darwin 目标会导致链接失败）。
>
> 构建后自动校验 PE 子系统标志（防回归）：
>
> ```bash
> go run ./scripts/pecheck contextloom-gui-windows-amd64.exe   # 应输出 Subsystem=2 (GUI)
> ```
>
> Makefile 捷径（自动跑 pecheck）：
>
> ```bash
> make build-gui-win      # 交叉编译 Windows GUI + PE 校验
> make verify-gui         # 校验已产出二进制的 PE 子系统标志
> ```

> 💡 **PowerShell 等价命令（Windows 本机，amd64）**——bash 的 `VAR=x` 前缀与 `\` 续行在 PowerShell/CMD 中不可用：
>
> ```powershell
> $env:CGO_ENABLED="1"; $env:GOOS="windows"; $env:GOARCH="amd64"
> go build -tags production -ldflags="-s -w -H windowsgui" -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui
> Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
> ```
>
> 完整 PowerShell/CMD 矩阵（含 arm64 目标、CMD `set X=Y&&` 尾随空格坑、续行符写法）见 [docs/build.zh-CN.md §3.2](docs/build.zh-CN.md)。

#### Linux（本机构建，需 WebKitGTK 开发库）

```bash
# 前置依赖（Debian/Ubuntu）
sudo apt install libgtk-3-dev libwebkit2gtk-4.0-dev gcc pkg-config
# 前置依赖（Fedora）
sudo dnf install gtk3-devel webkit2gtk4.0-devel gcc pkgconf-pkg-config

# 构建
CGO_ENABLED=1 go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui
```

> Linux 无 WebView 的 headless 服务器上，GUI 外壳自动回退浏览器模式。

### 构建矩阵速查

| 目标 | CGO | 额外依赖 | 关键标志 |
|---|---|---|---|
| TUI 任意平台 | `CGO_ENABLED=0` | 无 | `-ldflags="-s -w"` |
| GUI macOS | `CGO_ENABLED=1` | Xcode CLT | `-tags production`（可选 `CGO_CFLAGS="-Wno-deprecated-declarations"` 屏蔽弃用告警） |
| GUI Windows amd64 | `CGO_ENABLED=1` | mingw-w64 | `-tags production -ldflags="… -H windowsgui"` |
| GUI Windows arm64 | `CGO_ENABLED=1` | aarch64 mingw-w64 | 同上 |
| GUI Linux | `CGO_ENABLED=1` | WebKitGTK 开发库 + gcc | `-tags production` |

## 快速开始

```bash
git clone https://github.com/liaocatcat/contextloom.git
cd contextloom

# 1. 配置
cp config.example.yaml config.yaml   # 填入你的 endpoint / api_key

# 2. 运行（TUI）
make build && ./contextloom

# 或直接从源码运行
go run ./cmd/contextloom
```

把 `models[0].endpoint` 指向任意 [OI] 兼容服务，例如本地 llama.cpp 的 `http://127.0.0.1:8080/v1`。API Key 可通过 `OPENAI_API_KEY` 提供（使用 `deepseek` 通道时为 `DEEPSEEK_API_KEY`）。

## CLI 用法

```bash
contextloom                          # 交互式 TUI
contextloom --model <id>             # 指定启动模型预设
contextloom --resume                 # 继续最近一次会话
contextloom --memory-load <uuid>     # 加载指定历史会话
contextloom --memory-list            # 列出所有历史会话摘要
contextloom --memory-delete <uuid>   # 删除指定会话
contextloom --serve [--serve-port N] # headless 服务模式（GUI 启动器拉起）
contextloom --acp [--acp-port 20250] # ACP 服务模式
contextloom --mcp-server             # MCP Server（stdio JSON-RPC，只读暴露检索/记忆/技能）
contextloom --config <path>          # 指定配置文件
contextloom trace --tail 50          # 查看 JSONL 执行 trace
contextloom eval --file eval.yaml [--dry-run|--real] [--json out.json]   # 离线评估
contextloom eval-retrieval --file retrieval.yaml [--json out.json]       # 检索质量评测
contextloom --version
```

### TUI 命令速查（`/` 进入命令模式）

| 命令 | 说明 |
|---|---|
| `/mode chat\|task\|goal` | 切换工作模式 |
| `/goal [状态\|abort\|list\|resume <id>]` | 目标循环管理 |
| `/model <名称>` | 切换模型预设（无参列出） |
| `/effort <low\|medium\|high>` | 思考深度（默认 max） |
| `/top_p` / `/temperature` | 采样参数 |
| `/ctx` | 上下文用量/阈值/压缩审计（`/ctx reset` 重置摘要） |
| `/compress` | 手动压缩上下文（衰减未引用块） |
| `/inject <id>` | 手动提升上下文块至锚定区 |
| `/threshold <n>` | Harness 相关度评分阈值 |
| `/overseer_threshold <n>` | 监工通过阈值 |
| `/emp [名 [任务]]` | 员工中心：列员工/看档案/指派任务 |
| `/split <任务1>, <任务2>` | 强制并行派发子 Agent |
| `/scene` | 场景选择 |
| `/skills` / `/market [install <name>]` | 技能选择 / 技能市场 |
| `/memory` / `/dream` / `/teach <内容>` | 记忆统计 / 自动整理教训 / 手动沉淀 |
| `/sessions` / `/session <id>` | 列出 / 加载历史会话 |
| `/checkpoints` | 多 Agent 运行检查点列表 |
| `/sandbox [commit\|discard]` | 沙箱状态 / 合并写回 / 丢弃 |
| `/vocab [list\|accept\|reject]` | 词汇映射提案管理 |
| `/plugin list` | 插件清单 |
| `/trace [N]` | 最近 N 条 trace |
| `/config` | 查看生效配置 |
| `/acp` | ACP 服务状态 |
| `/quit` | 优雅退出并持久化 |

## 配置

`config.yaml`（完整注释见 [`config.example.yaml`](config.example.yaml)）定义：

- `models` —— 模型预设（name/backend/endpoint/api_key/max_context/vision_model/supports_vision/send_effort 等）。
- `gateway` —— 路由规则与 Key 池（不配置 = 行为与单模型完全一致）。
- `context` —— 三档压缩阈值与 keep_recent。
- `harness` —— 评分阈值 / 衰减因子 / 锚定区占比。
- `overseer` —— 通过阈值 / 自动计划节点 / 测试 TEE / 临时文档策略。
- `sandbox` / `tools` —— 影子文件系统、网络隔离、审批预设。
- `memory` / `project` / `plugins` —— 会话存储、项目根、插件目录。
- `trace` —— 本地 JSONL + OTLP 导出端点。
- `channels` —— webhook / cron 触发。
- `mcp` —— 外部 MCP server 接入。
- `preview` —— GUI 预览的 PlantUML 渲染服务。

项目级数据存放在 `.contextloom/` 目录：

```
.contextloom/
├── settings.json     运行时设置（模型/effort/mode）
├── memory/sessions/  <uuid>.json + <uuid>.jsonl 事件日志
├── employees/        员工档案（含 ONTOLOGY.md 经验蒸馏）
├── scenes/           场景预设
├── chains/           工具链定义
├── gates/            质量门禁（目录存在即启用）
├── goals/            goal 循环状态与检查点
├── checkpoints/      多 Agent 运行检查点
├── harness/          检索索引持久化（index.json）
├── plugins/          项目级插件（与用户级合并）
├── skills/           已安装技能
├── trace/            JSONL trace（按日轮转）
└── sandbox/          沙箱影子文件系统
```

## 架构

```
┌────────────┐  ┌──────────────┐  ┌─────────────┐
│  TUI       │  │  GUI (Wails) │  │ MCP / ACP   │
└─────┬──────┘  └──────┬───────┘  └──────┬──────┘
      └──────────┬─────┴──────────────────┘
          ┌──────▼───────┐
          │   runtime    │  编排、监工、goal 状态机、检查点
          └──────┬───────┘
   ┌─────────────┼──────────────┐
┌──▼───┐   ┌────▼─────┐   ┌────▼─────┐
│ ctxmgr│   │ executor │   │  memory  │
│ 预算  │   │ 子智能体  │   │ JSONL 日志│
└──┬────┘   └────┬─────┘   └──────────┘
   │        ┌────▼─────┐
   │        │  model   │  Provider 网关（[OI] 兼容 / DeepSeek）
   └────────┤ gateway  │
            └──────────┘
         ┌──────────┐
         │ harness  │  混合检索（倒排+符号+语义），压缩原文找回
         └──────────┘
```

## 文档索引

| 页面 | 内容 |
|---|---|
| [docs/architecture.md](docs/architecture.md) | 分层架构：UI 外壳、runtime、ctxmgr、executor、网关、记忆、沙箱、可观测性。 |
| [docs/context-engine.md](docs/context-engine.md) | 上下文精细管理（三档阈值、五步压缩、滚动摘要）与 `harness_search` 混合检索引擎深读。 |
| [docs/design-philosophy.zh-CN.md](docs/design-philosophy.zh-CN.md) | 设计哲学大白话——模型 5 类固有局限，以及针对每一条的机制性回答。 |
| [docs/comparison.zh-CN.md](docs/comparison.zh-CN.md) | 与裸跑 LLM、通用 Agent 框架（LangChain/LangGraph/AutoGPT/CrewAI）、编码智能体（Claude Code/Cursor/OpenHands/aider）的结构性、可核查对比。 |
| [docs/build.zh-CN.md](docs/build.zh-CN.md) | TUI（纯 Go，全平台）与 GUI（Wails/CGO，逐平台）完整交叉编译矩阵，附常见坑与 Docker 示例。 |
| [docs/evaluation.zh-CN.md](docs/evaluation.zh-CN.md) | 可观测、可评估与可审计：JSONL trace、OTLP 导出、`contextloom eval` / `eval-retrieval`（Recall@K / MRR）、质量账本与证据链。 |
| [docs/plugins.md](docs/plugins.md) | 插件体系：员工、场景、工具链、门禁、Manifest 插件及发现/合并顺序。 |
| [docs/toolchain.md](docs/toolchain.md) | 员工 / 场景 / 工具链 / 门禁如何组合成受监工把关的工作台。 |

英文版：同上各页的英文版（architecture / context-engine / design-philosophy / comparison / build / evaluation / plugins / toolchain）。

## 开发

```bash
gofmt -w .          # CI 强制格式检查
go vet ./...
go test -race ./...
```

CI 在 Linux/macOS/Windows（Go 1.22）上运行 gofmt 检查、`go vet`、构建与测试。AI 协作开发约束见 [AGENTS.md](AGENTS.md)，贡献流程见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可证

[MIT](LICENSE)
