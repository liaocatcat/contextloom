<div align="center">

# ContextLoom

**上下文感知的编码智能体 Harness——为你的 LLM 编织恰到好处的上下文，终端与桌面双形态。**

[English](README.md) | [中文](README.zh-CN.md)

Go 1.22+ · macOS / Linux / Windows · MIT 许可证

</div>

---

ContextLoom 是一个用 Go 编写的编码智能体 Harness（驾驶舱）。它通过终端（TUI）、原生桌面窗口（GUI）或任何兼容 MCP/ACP 的外部工具驱动 LLM，同时由上下文引擎保证每次请求的信息密度：长对话按预算压缩，项目记忆跨会话持久，子智能体的产出要经过评分门禁审查后才会呈现给你。

## 特性

- **上下文引擎** —— token 预算、滚动摘要与自动压缩，让长任务始终留在上下文窗口内且不丢线索。
- **长任务 JSON 结构化状态** —— 任务/目标状态以结构化 JSON 管理而非自然语言散文，压缩与多智能体交接后依然可靠。
- **监工把关的子智能体** —— 内置监工（overseer）按可配置质量门禁给每份子智能体产出打分；不达标的产出会被打回，绝不外显。
- **多 Provider 模型网关** —— 任意 OpenAI 兼容端点（llama.cpp、ollama、vLLM、本地、云端）加可选 DeepSeek 通道；基于规则的路由（`role`/`task`/`budget`）、多 Key 池化、熔断自动降级。
- **一切皆插件** —— 员工（角色预设）、场景、工具链、质量门禁全部是声明式 YAML；项目级与用户级插件目录自动合并。
- **MCP 客户端与服务端** —— 消费外部 MCP 工具，或把 ContextLoom 自身暴露为 MCP Server，供 Claude Code、Cursor 等调用。
- **持久记忆** —— 会话以追加式 JSONL 事件日志存储；`--memory-load` 恢复任意会话（含压缩快照）。
- **工具沙箱** —— 网络/写文件策略、写操作影子文件系统、单次运行内存限额。
- **可复现评估** —— 离线智能体级评估 harness（`eval.yaml`），基于 trace 评分。
- **双 UI、同一内核** —— 键盘优先的 TUI + 桌面 GUI 外壳（Wails/WebView），无桌面环境时自动回退浏览器模式。

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

把 `models[0].endpoint` 指向任意 OpenAI 兼容服务，例如本地 llama.cpp 的 `http://127.0.0.1:8080/v1`。API Key 可通过 `OPENAI_API_KEY` 提供（使用 `deepseek` 通道时为 `DEEPSEEK_API_KEY`）。

## 构建

不想用 make？裸 go 命令完全可用：

```bash
go build -ldflags="-s -w" -o contextloom ./cmd/contextloom   # 当前平台（TUI）
go test -race -cover ./...                                    # 测试
```

或使用 Makefile（封装同样的命令，外加项目专属校验）：

```bash
make build    # 当前平台二进制（TUI）
make test     # 测试（-race + 覆盖率）
make lint     # golangci-lint
```

交叉编译是 Go 原生能力——设置 `GOOS`/`GOARCH` 即可，例如 `GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-linux-arm64 ./cmd/contextloom`。

### 桌面 GUI（可选）

```bash
make build-gui          # macOS / Linux（Linux 需 WebKitGTK）
make build-gui-win      # 交叉编译 Windows GUI（需 mingw-w64；自动校验 PE 子系统标志）
make build-gui-macos    # 打包 macOS .app
make verify-gui         # 校验 GUI PE 子系统标志
```

裸 go 等价命令（也是推荐用 make 的原因）：GUI 构建需要 `CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 go build -tags production -ldflags="-s -w -H windowsgui"`——漏掉 `-H windowsgui` 会产出 console 子系统 exe，启动时弹出黑色命令行窗口。Makefile 还会自动跑 `scripts/pecheck` 校验 PE 子系统标志，防止回归。

GUI 外壳会从自身所在目录拉起 `contextloom` 后端（或通过 `CONTEXTLOOM_BIN` 指定），请把两个二进制放在同一目录。

## 使用

```bash
contextloom                    # 交互式 TUI
contextloom --resume           # 继续最近一次会话
contextloom --memory-load <id> # 加载指定会话
contextloom trace --tail 50    # 查看 JSONL 执行 trace
contextloom --serve            # ACP 服务模式
contextloom --mcp              # 以 MCP Server 运行（stdio JSON-RPC）
```

TUI 中按 `/` 进入命令模式：`/model`、`/mode`（`chat` / `task` / `goal`）、`/trace`、`/resume` 等；`?` 查看全部快捷键。

## 配置

`config.yaml`（完整注释见 [`config.example.yaml`](config.example.yaml)）定义：

- `models` —— 模型预设（name/backend/endpoint/api_key/max_context 等）；`/model <名称>` 热切换。
- `gateway` —— 可选的路由规则与 Key 池：`role=reviewer → strong-model`、`task=vision → vision-model`、`budget<=low → cheap-model`。
- `sandbox`、`memory`、`plugins`、`project` —— 工具隔离、会话存储、插件目录、项目根。

项目级数据存放在 `.contextloom/` 目录（会话、插件、工具链、门禁、trace），项目自包含、可随仓库共享。

## 架构

```
┌────────────┐  ┌──────────────┐  ┌─────────────┐
│  TUI       │  │  GUI (Wails) │  │ MCP / ACP   │
└─────┬──────┘  └──────┬───────┘  └──────┬──────┘
      └──────────┬─────┴──────────────────┘
          ┌──────▼───────┐
          │   runtime    │  编排、监工、目标管理
          └──────┬───────┘
   ┌─────────────┼──────────────┐
┌──▼───┐   ┌────▼─────┐   ┌────▼─────┐
│ ctxmgr│   │ executor │   │  memory  │
│ 预算  │   │ 子智能体  │   │ JSONL 日志│
└──┬────┘   └────┬─────┘   └──────────┘
   │        ┌────▼─────┐
   │        │  model   │  Provider 网关（OpenAI 兼容 / DeepSeek）
   └────────┤ gateway  │
            └──────────┘
```

完整介绍见 [docs/architecture.md](docs/architecture.md)，插件体系见 [docs/plugins.md](docs/plugins.md)，员工/场景/工具链/门禁见 [docs/toolchain.md](docs/toolchain.md)。

## 开发

```bash
gofmt -w .          # CI 强制格式检查
go vet ./...
go test -race ./...
```

CI 在 Linux/macOS/Windows（Go 1.22）上运行 gofmt 检查、`go vet`、构建与测试。详见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可证

[MIT](LICENSE)
