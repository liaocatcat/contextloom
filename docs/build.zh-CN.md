# 从源码构建 ContextLoom（全平台矩阵）

本页是 ContextLoom 完整、可直接复制粘贴的构建参考。覆盖 **TUI**（纯 Go，零 CGO）与 **GUI** 桌面外壳（Wails/WebView，需 CGO）在所有受支持目标平台上的构建——不省略、不藏标志。

> 相关页面：[../README.zh-CN.md](../README.zh-CN.md)（项目总览）·
> [architecture.md](architecture.md)（分层与运行时）·
> [toolchain.md](toolchain.md)（员工/场景/工具链/门禁）。
> 英文版：[build.md](build.md)。

> 💡 **Shell 语法提示**：本页其余命令默认为 **bash** 语法（`VAR=x cmd` 变量前缀 + 反斜杠续行）。Windows 原生用户有两种选择：① 在 WSL / MSYS2 / Git Bash 中照抄 bash 版本；② 使用下文 §2.1 / §2.2 / §3.2 给出的 **PowerShell / CMD 等价命令**（变量前置写法已改为 `$env:` 赋值 / `set`，不使用续行符）。

## 0. 速览

```bash
# TUI —— 当前平台，一条命令：
make build

# TUI —— 任意目标（纯 Go，无需额外工具链）：
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-linux-arm64 ./cmd/contextloom

# GUI —— 当前平台（需 C 工具链 + WebView 开发库）：
# macOS 15+ 建议带 CGO_CFLAGS 屏蔽 Wails 的弃用告警（无害，见 §3.1）：
CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" \
  go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui

# GUI —— 从任意主机交叉编译 Windows（需 mingw-w64；-H windowsgui 必带）：
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui
```

两个构建单元，两条规则：

| 单元 | 源码 | CGO | 能否交叉编译？ |
|---|---|---|---|
| TUI `contextloom` | `./cmd/contextloom` | **关**（`CGO_ENABLED=0`） | 可以，覆盖所有 Go 目标 |
| GUI `contextloom-gui` | `./cmd/contextloom-gui` | macOS/Linux **开**、Windows **关** | macOS/Linux 需匹配的交叉 C 工具链；Windows 为纯 Go COM 绑定，任意宿主可编译 |

## 1. 前置条件

- **Go 1.22+**（`go version`）。模块为 `github.com/contextloom/contextloom-agent`（见 `go.mod`）。
- **TUI**：无需其他。纯 Go，无 C 工具链、无系统库。
- **GUI**：C 编译器 + 平台 WebView 头文件：
  - macOS：Xcode Command Line Tools（`xcode-select --install`）。
  - Windows：**无需任何东西**——Windows GUI 是纯 Go（COM 绑定），不需要 C 工具链，也不需要 mingw-w64。
  - Linux：GTK 3 + WebKitGTK 2 开发包（见 §3.3）。

一次性拉取依赖：

```bash
go mod download
```

## 2. TUI 核心（纯 Go —— 所有平台 `CGO_ENABLED=0`）

TUI（`./cmd/contextloom`）是纯 Go 实现，天然支持交叉编译。以下命令格式统一：

```
CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build -ldflags="-s -w" -o contextloom-<os>-<arch>[.exe] ./cmd/contextloom
```

### 2.1 完整交叉编译矩阵

**macOS**

```bash
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-darwin-arm64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-darwin-amd64 ./cmd/contextloom
```

**Linux —— x86 与 ARM 家族**

```bash
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-linux-amd64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-linux-arm64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=386   go build -ldflags="-s -w" -o contextloom-linux-386     ./cmd/contextloom

# 32 位 ARM：用 GOARM 选择 ARM 版本（5 = ARMv5，6 = ARMv6，7 = ARMv7）
CGO_ENABLED=0 GOOS=linux   GOARCH=arm GOARM=5 go build -ldflags="-s -w" -o contextloom-linux-armv5 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm GOARM=6 go build -ldflags="-s -w" -o contextloom-linux-armv6 ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o contextloom-linux-armv7 ./cmd/contextloom
```

**Linux —— 其他架构**

```bash
CGO_ENABLED=0 GOOS=linux   GOARCH=ppc64le  go build -ldflags="-s -w" -o contextloom-linux-ppc64le  ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=s390x    go build -ldflags="-s -w" -o contextloom-linux-s390x    ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=riscv64  go build -ldflags="-s -w" -o contextloom-linux-riscv64  ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=loong64  go build -ldflags="-s -w" -o contextloom-linux-loong64  ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=mips64le go build -ldflags="-s -w" -o contextloom-linux-mips64le ./cmd/contextloom
CGO_ENABLED=0 GOOS=linux   GOARCH=mipsle   go build -ldflags="-s -w" -o contextloom-linux-mipsle   ./cmd/contextloom
```

**Windows**

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-windows-amd64.exe ./cmd/contextloom
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-windows-arm64.exe ./cmd/contextloom
CGO_ENABLED=0 GOOS=windows GOARCH=386   go build -ldflags="-s -w" -o contextloom-windows-386.exe   ./cmd/contextloom
```

**Windows 本机 —— PowerShell / CMD**（bash 的 `VAR=x cmd` 前缀语法在 PowerShell/CMD 中不可用，须改为 `$env:` 赋值 / `set`）：

```powershell
$env:CGO_ENABLED="0"; $env:GOOS="windows"; $env:GOARCH="amd64"
go build -ldflags="-s -w" -o contextloom-windows-amd64.exe ./cmd/contextloom

$env:GOOS="windows"; $env:GOARCH="arm64"
go build -ldflags="-s -w" -o contextloom-windows-arm64.exe ./cmd/contextloom

$env:GOOS="windows"; $env:GOARCH="386"
go build -ldflags="-s -w" -o contextloom-windows-386.exe ./cmd/contextloom

Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED   # 清理，避免污染后续构建
```

```cmd
set CGO_ENABLED=0&& set GOOS=windows&& set GOARCH=amd64&& go build -ldflags="-s -w" -o contextloom-windows-amd64.exe .\cmd\contextloom
set CGO_ENABLED=0&& set GOOS=windows&& set GOARCH=arm64&& go build -ldflags="-s -w" -o contextloom-windows-arm64.exe .\cmd\contextloom
set CGO_ENABLED=0&& set GOOS=windows&& set GOARCH=386&& go build -ldflags="-s -w" -o contextloom-windows-386.exe .\cmd\contextloom
```

> ⚠️ **CMD 的 `set X=Y&&` 等号后不要留尾随空格**：CMD 会把空格并入变量值（`GOOS=windows ` 这种带尾空格的值会导致 Go 报 `unknown GOOS`）。上例故意写成 `set GOOS=windows&&`（`&&` 紧贴值），且**不要**在 `set` 行末手动补空格。

> ⚠️ **TUI 的 Windows 构建不要加 `-H windowsgui`。** TUI 是**控制台**程序。`-H windowsgui` 会把 PE 切到 GUI 子系统（Subsystem=2），从而脱离控制台、破坏全部终端输入输出。只有 GUI 二进制才用该标志（§3.2）。

**BSD 家族**

```bash
CGO_ENABLED=0 GOOS=freebsd   GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-freebsd-amd64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=freebsd   GOARCH=arm64 go build -ldflags="-s -w" -o contextloom-freebsd-arm64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=netbsd    GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-netbsd-amd64    ./cmd/contextloom
CGO_ENABLED=0 GOOS=openbsd   GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-openbsd-amd64   ./cmd/contextloom
CGO_ENABLED=0 GOOS=dragonfly GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-dragonfly-amd64 ./cmd/contextloom
```

**Solaris、AIX**

```bash
CGO_ENABLED=0 GOOS=solaris GOARCH=amd64 go build -ldflags="-s -w" -o contextloom-solaris-amd64 ./cmd/contextloom
CGO_ENABLED=0 GOOS=aix     GOARCH=ppc64 go build -ldflags="-s -w" -o contextloom-aix-ppc64     ./cmd/contextloom
```

> ⚠️ **不支持 Plan 9。** 上游终端依赖（`github.com/charmbracelet/x/term`）没有 plan9 实现，
> TUI 链接会报 `undefined: state / isTerminal / makeRaw / getState`。除非替换终端层，否则
> 无解，故 plan9 已从构建矩阵中移除。

### 2.2 一键批量构建（全部 TUI 目标）

用 shell 循环遍历 `GOOS/GOARCH` 表。`.exe` 后缀仅用于 Windows。

```bash
#!/usr/bin/env bash
# build-all-tui.sh —— 把 TUI 构建到当前目录下所有受支持目标
set -euo pipefail

# "GOOS/GOARCH[/GOARM]" 条目
targets=(
  darwin/amd64   darwin/arm64
  linux/amd64    linux/arm64   linux/386
  linux/arm/5    linux/arm/6   linux/arm/7
  linux/ppc64le  linux/s390x   linux/riscv64   linux/loong64
  linux/mips64le linux/mipsle
  windows/amd64  windows/arm64 windows/386
  freebsd/amd64  freebsd/arm64
  netbsd/amd64   openbsd/amd64 dragonfly/amd64
  solaris/amd64  aix/ppc64
)

for t in "${targets[@]}"; do
  IFS='/' read -r os arch arm <<< "$t"
  out="contextloom-${os}-${arch}"
  [ "$arch" = "arm" ] && [ -n "${arm:-}" ] && out="contextloom-${os}-armv${arm}"
  [ "$os" = "windows" ] && out="${out}.exe"

  echo "==> ${os}/${arch}${arm:+/GOARM=$arm} -> ${out}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" ${arm:+GOARM="$arm"} \
    go build -ldflags="-s -w" -o "$out" ./cmd/contextloom
done
echo "✅ TUI 构建完成。"
```

**PowerShell 等价循环**（Windows 本机用户；逻辑与上方 bash 版一致，Windows 目标自动补 `.exe`）：

```powershell
# build-all-tui.ps1 —— 把 TUI 构建到当前目录下所有受支持目标

$targets = @(
  "darwin/amd64",  "darwin/arm64",
  "linux/amd64",   "linux/arm64",  "linux/386",
  "linux/arm/5",   "linux/arm/6",  "linux/arm/7",
  "linux/ppc64le", "linux/s390x",  "linux/riscv64", "linux/loong64",
  "linux/mips64le","linux/mipsle",
  "windows/amd64", "windows/arm64","windows/386",
  "freebsd/amd64", "freebsd/arm64",
  "netbsd/amd64",  "openbsd/amd64","dragonfly/amd64",
  "solaris/amd64", "aix/ppc64",    "plan9/amd64"
)

foreach ($t in $targets) {
  $p = $t -split '/'
  $os = $p[0]; $arch = $p[1]
  $arm = if ($p.Count -gt 2) { $p[2] } else { $null }
  $out = "contextloom-$os-$arch"
  if ($arm) { $out = "contextloom-$os-armv$arm" }
  if ($os -eq "windows") { $out = "$out.exe" }

  Write-Host "==> $os/$arch$(if ($arm) { "/GOARM=$arm" }) -> $out"
  $env:CGO_ENABLED = "0"; $env:GOOS = $os; $env:GOARCH = $arch
  if ($arm) { $env:GOARM = $arm } else { Remove-Item Env:GOARM -ErrorAction SilentlyContinue }
  go build -ldflags="-s -w" -o $out ./cmd/contextloom
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
Write-Host "✅ TUI 构建完成。"
```

### 2.3 Makefile 封装

```bash
make build   # 当前平台 TUI 二进制 -> ./contextloom
make run     # go run ./cmd/contextloom
make test    # go test -race -cover ./...
make lint    # golangci-lint run ./...
make clean   # rm -f contextloom
```

## 3. GUI 桌面外壳（Wails/WebView —— 需 CGO）

GUI（`./cmd/contextloom-gui`）是 Wails v2 桌面外壳。它**不是**独立应用：启动时会定位 `contextloom` 后端（见 §6），必要时以 headless 方式拉起，然后渲染前端。由于 Wails 绑定平台 WebView，它需要 CGO 与各平台的 C 工具链——无法在目标平台之外（或没有其工具链时）产出 macOS/Linux 的 GUI 二进制。

生产构建标签为 `-tags production`（剥离 Wails 开发服务器）。

### 3.1 macOS（本机构建）

```bash
# 当前 macOS 架构的 GUI 二进制
# CGO_CFLAGS 仅用于屏蔽 Wails 的弃用告警（无害，见下方 ⚠️）：
CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" \
  go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui

# 显式指定架构（在匹配主机上构建，或用 C 交叉工具链）
CGO_ENABLED=1 GOARCH=arm64 go build -tags production -ldflags="-s -w" -o contextloom-gui-darwin-arm64 ./cmd/contextloom-gui
CGO_ENABLED=1 GOARCH=amd64 go build -tags production -ldflags="-s -w" -o contextloom-gui-darwin-amd64 ./cmd/contextloom-gui

# 通用二进制（用 lipo 合并两个切片）
lipo -create -output contextloom-gui-darwin-universal \
  contextloom-gui-darwin-arm64 contextloom-gui-darwin-amd64

# 打包可双击的 .app -> ./ContextLoom.app
make build-gui-macos          # 内部执行 scripts/build-macos-app.sh
```

> ⚠️ **macOS 15+ 的弃用告警无害，别当报错。** 在 macOS 15 及以上（实测宿主 macOS 27.0 + Xcode CLT）构建 GUI 时，会打印：
>
> ```
> WailsContext.m:164:18: warning: 'setShowsBaselineSeparator:' is deprecated:
> first deprecated in macOS 15.0 - No longer supported [-Wdeprecated-declarations]
> ```
>
> 这是第三方依赖 **Wails v2.9.2** 调用了 macOS 15 起弃用的 AppKit API 所产生的**警告（warning）**，不是错误：构建仍 `exit 0` 并产出可用二进制（实测输出版本号 ContextLoom GUI v2.4.0）。**所有 Wails v2 版本（含 v2.16.0）都仍调用该 API，升级 Wails 无法消除。** 如需让日志清爽，加 `CGO_CFLAGS` 关闭该类告警即可（已验证构建仍成功、告警消失）：
>
> ```bash
> CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" \
>   go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui
> ```

`scripts/build-macos-app.sh` 会把 `contextloom` 与 `contextloom-gui` 一起构建进 `./ContextLoom.app/Contents/MacOS/`，写入 `Info.plist`，并标记可执行。在 macOS 上，双击**裸**二进制会让 LaunchServices 用 Terminal.app 打开；`.app` 包则以正规 GUI 应用方式启动，无控制台窗口。

### 3.2 Windows（无需 C 工具链——纯 Go COM 绑定）

**不需要 mingw-w64。** Wails v2 的 Windows 后端是纯 Go 实现（`wailsapp/go-webview2`，
对 WebView2 运行时的 COM 绑定）；模块里带 `import "C"` 的文件全部是 `*_darwin.go`。
因此 Windows GUI 在**任意宿主**（Windows/macOS/Linux）以 `CGO_ENABLED=0` 即可构建，
完全不需要交叉 C 工具链。（本文档早期版本称必须装 mingw-w64，是**误传**——实测传入
bogus `CC` 仍构建成功，证明该路径根本不调用 C 编译器。）

```bash
# Windows amd64 —— 任意宿主，无需 mingw：
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui

# Windows on ARM64 —— 同样无需 aarch64 mingw：
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 \
  go build -tags production -ldflags="-s -w -H windowsgui" \
  -o contextloom-gui-windows-arm64.exe ./cmd/contextloom-gui

# Makefile 捷径（交叉编译 amd64 + PE 校验）：
make build-gui-win
```

**Windows 本机 —— PowerShell / CMD**（上述 bash 的 `VAR=x` 前缀与反斜杠续行在 PowerShell/CMD 中不可用；`-H windowsgui` 必带，含义见下方 ⚠️）：

PowerShell（推荐单行，避免反引号续行符在复制粘贴中出错的坑）：

```powershell
$env:CGO_ENABLED="0"; $env:GOOS="windows"; $env:GOARCH="amd64"
go build -tags production -ldflags="-s -w -H windowsgui" -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui

Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED   # 清理，避免污染后续构建
```

多行续行写法（PowerShell 用反引号 `` ` ``）：

```powershell
$env:CGO_ENABLED="0"; $env:GOOS="windows"; $env:GOARCH="amd64"
go build -tags production -ldflags="-s -w -H windowsgui" `
  -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui
```

CMD（单行；`set X=Y&&` 等号后不留尾随空格，否则空格会进入变量值）：

```cmd
set CGO_ENABLED=1&& set GOOS=windows&& set GOARCH=amd64&& go build -tags production -ldflags="-s -w -H windowsgui" -o contextloom-gui-windows-amd64.exe .\cmd\contextloom-gui
```

> ⚠️ **`-H windowsgui` 是生死标志。** Windows GUI 构建漏掉它会产出 console 子系统 exe（PE `Subsystem=3`），双击必弹黑色命令行窗口。该标志由 Go **链接器按目标 `GOOS=windows` 生效**，与构建主机无关——因此从 Linux/macOS 交叉编译时，与在 Windows 本机构建同样必须带。
>
> 反过来说，**在 macOS/Linux 宿主上构建 GUI 时不要加 `-H windowsgui`**——它只对 `GOOS=windows` 有效，加到 darwin 目标上会导致链接失败（详见 §7 排错表）。
>
> 历史教训：早期 Makefile 用 `ifeq ($(OS),Windows_NT)` 门控该标志，导致交叉编译或裸 `go build` 产出 Windows exe 时该标志被静默丢弃。当前 Makefile 在 `build-gui-win`（Windows 交叉编译路径）无条件应用 `GUI_LDFLAGS := -H windowsgui`；而宿主目标 `build-gui` 已改为宿主感知——仅当构建宿主为 Windows 时才加该标志，macOS/Linux 宿主不加（否则链接失败，详见 §7）。

### 3.3 Linux（本机构建——需 WebKitGTK 开发库）

```bash
# 前置依赖 —— Debian / Ubuntu：
sudo apt update
sudo apt install libgtk-3-dev libwebkit2gtk-4.0-dev gcc pkg-config

# 前置依赖 —— Fedora / RHEL：
sudo dnf install gtk3-devel webkit2gtk4.0-devel gcc pkgconf-pkg-config

# 前置依赖 —— Arch：
sudo pacman -S gtk3 webkit2gtk base-devel pkgconf

# 构建
CGO_ENABLED=1 go build -tags production -ldflags="-s -w" -o contextloom-gui ./cmd/contextloom-gui
```

> 在无显示/无 WebView 的 headless Linux 主机上，GUI 外壳自动回退到**浏览器模式**——启动后端并通过 HTTP 提供 UI，在系统浏览器中打开而非桌面窗口。

### 3.4 PE 子系统校验（防回归）

任何 Windows GUI 构建都应校验：PE `Subsystem` 必须为 **2（GUI）**，而非 3（CONSOLE）。

```bash
go run ./scripts/pecheck contextloom-gui-windows-amd64.exe        # 期望：subsystem=2(GUI) OK
go run ./scripts/pecheck contextloom-gui-windows-amd64.exe 2      # 显式期望值
```

`scripts/pecheck` 直接读取 PE 可选头的 `Subsystem` 字段（偏移 68），不匹配时以非零码退出——因此丢失 `-H windowsgui` 会让构建失败，而不是交付一个会弹出控制台的窗口化应用。

Makefile 自动跑 `pecheck`：

```bash
make build-gui       # 构建 contextloom-gui（本机）后校验 PE 子系统
make build-gui-win   # 交叉编译 Windows GUI 后校验 PE 子系统
make verify-gui      # 校验已产出的 contextloom-gui.exe
```

## 4. 构建矩阵速查

| 目标 | CGO | 额外依赖 | 关键标志 |
|---|---|---|---|
| TUI，任意平台 | `CGO_ENABLED=0` | 无 | `-ldflags="-s -w"` |
| GUI，macOS（arm64/amd64） | `CGO_ENABLED=1` | Xcode CLT | `-tags production`（可选 `CGO_CFLAGS="-Wno-deprecated-declarations"` 屏蔽弃用告警） |
| GUI，macOS 通用 | `CGO_ENABLED=1` | Xcode CLT + `lipo` | `-tags production` + `lipo -create` |
| GUI，Windows amd64 | `CGO_ENABLED=0` | **无** | `-tags production -ldflags="… -H windowsgui"` |
| GUI，Windows arm64 | `CGO_ENABLED=0` | **无** | 同上 |
| GUI，Linux | `CGO_ENABLED=1` | `libgtk-3-dev`、`libwebkit2gtk-4.0-dev`、`pkg-config` | `-tags production` |

## 5. Makefile 目标速查

| 目标 | 作用 |
|---|---|
| `make build` | 构建当前平台 TUI（`./contextloom`）。 |
| `make run` | `go run ./cmd/contextloom`。 |
| `make test` | `go test -race -cover ./...`。 |
| `make lint` | `golangci-lint run ./...`。 |
| `make clean` | 删除本地 `contextloom` 二进制。 |
| `make build-gui` | 为本机构建 GUI。宿主感知：Windows 宿主会加 `-H windowsgui` 并跑 `pecheck`；macOS/Linux 宿主不加该标志且跳过 `pecheck`（非 PE 目标）。 |
| `make build-gui-win` | 交叉编译 Windows GUI（amd64，纯 Go，无需 mingw）+ `pecheck`。 |
| `make verify-gui` | 对已产出的 `contextloom-gui.exe` 跑 `pecheck`。 |
| `make build-gui-macos` | 通过 `scripts/build-macos-app.sh` 打包 `./ContextLoom.app`。 |

## 6. 运行时布局与环境变量

```
deploy/
  contextloom-gui        # GUI 启动器（Wails 桌面外壳）
  contextloom            # GUI 按需拉起的后端
  config.yaml            # 可选；从 config.example.yaml 复制
```

- **同目录：** GUI 优先在自身可执行文件旁查找 `contextloom` 后端（`os.Executable()`），**请把两个二进制放在同一目录**。
- **`CONTEXTLOOM_BIN`：** 若后端在别处，显式指向它：

  ```bash
  CONTEXTLOOM_BIN=/opt/contextloom/contextloom ./contextloom-gui
  ```

  查找顺序为：**同目录 → `CONTEXTLOOM_BIN` → `PATH`**。
- **headless Linux 回退：** 无显示/WebView 时，外壳通过 HTTP 提供 UI 并打开系统浏览器（或显式用 `--browser` 运行）。
- **配置查找：** GUI 依次在真实（解析符号链接后）可执行文件旁、CWD、内置默认中解析 `config.yaml`。把 `config.example.yaml` 复制为 `config.yaml` 并填入模型/API Key。

## 7. 常见坑与排错

| 现象 | 原因 | 修复 |
|---|---|---|
| 双击 Windows GUI 弹出黑色控制台窗口 | 丢失 `-H windowsgui` → PE `Subsystem=3` | 带 `-H windowsgui` 重新构建；用 `go run ./scripts/pecheck <exe>` 校验（期望 Subsystem=2）。 |
| `gcc: command not found` / `cgo: C compiler not found` | CGO 关闭或无 C 工具链 | 设 `CGO_ENABLED=1` 并安装 gcc/Clang/mingw-w64。 |
| `Package webkit2gtk-4.0 was not found`（Linux） | 缺 WebKitGTK 开发头文件 | 安装 `libwebkit2gtk-4.0-dev`（Debian）/ `webkit2gtk4.0-devel`（Fedora）。 |
| 交叉编译 macOS/Linux GUI 失败 | CGO 交叉编译需匹配的交叉工具链 | 在目标 OS 上构建 GUI，或用正规交叉 C 工具链。**Windows** GUI 完全不需要 C 工具链（`CGO_ENABLED=0`）。 |
| 构建后 Windows TUI 无法交互 | 给 TUI 加了 `-H windowsgui` | 去掉——TUI 是控制台程序。 |
| GUI 启动时找不到 `contextloom` 后端 | 两二进制不在同一目录 | 放到同一目录，或设 `CONTEXTLOOM_BIN`。 |
| 老主板跑错 ARM 变体 | `GOARM` 不匹配 | ARMv5 用 `GOARM=5`，ARMv6 用 `6`，ARMv7 用 `7`。 |
| 经符号链接启动后 GUI 读到错误 `config.yaml` | 符号链接解析 | 启动器先解析真实可执行路径；把 `config.yaml` 放在真实二进制旁或 CWD。 |
| macOS 15+ 构建 GUI 打印 `setShowsBaselineSeparator:` is deprecated 警告 | 第三方依赖 Wails v2 调用了 macOS 15 起弃用的 AppKit API；**属警告非错误**，构建仍 exit 0 并产出可用二进制（所有 Wails v2 版本均如此，升级无法消除） | 无需处理；如需日志清爽，加 `CGO_CFLAGS="-Wno-deprecated-declarations"` 重新构建即可屏蔽该告警。 |
| `make build-gui` 在 macOS/Linux 宿主上链接失败（`libc_*: initialize bounds (0 < 8)` … `link: too many errors`） | Makefile 无条件加了 `-H windowsgui`，而该链接标志**仅对目标 `GOOS=windows` 有效**；在 darwin 宿主上用它链接失败 | macOS/Linux 宿主构建 GUI **不要**加 `-H windowsgui`（该标志只用于 Windows GUI 构建，见 §3.2）；直接用 §3.1 的 `go build` 命令或 `make build-gui-macos`。 |

## 8. Docker 构建示例

仓库未附带 `Dockerfile`，以下为多阶段示例。TUI 阶段只需 Go（纯 Go，`CGO_ENABLED=0`）；GUI 阶段额外需要 WebKitGTK 开发库。

```dockerfile
# syntax=docker/dockerfile:1

# ---- 阶段 1：构建 TUI（纯 Go，无 CGO）----
FROM golang:1.22-bookworm AS tui
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /out/contextloom ./cmd/contextloom

# ---- 阶段 2：构建 GUI（需 GTK3 + WebKitGTK）----
FROM golang:1.22-bookworm AS gui
RUN apt-get update && apt-get install -y --no-install-recommends \
      libgtk-3-dev libwebkit2gtk-4.0-dev gcc pkg-config \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags production -ldflags="-s -w" \
    -o /out/contextloom-gui ./cmd/contextloom-gui

# ---- 阶段 3：运行时（TUI + GUI 启动器）----
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      libgtk-3-0 libwebkit2gtk-4.0-37 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=tui /out/contextloom       /app/contextloom
COPY --from=gui /out/contextloom-gui   /app/contextloom-gui
WORKDIR /app
# headless 主机回退浏览器模式；TUI 可直接运行：
#   docker run -it --rm <image> /app/contextloom
ENTRYPOINT ["/app/contextloom-gui"]
```

构建与运行：

```bash
docker build -t contextloom:local .
docker run -it --rm contextloom:local /app/contextloom   # 改为运行 TUI
```

## 9. 相关文档

- [../README.zh-CN.md](../README.zh-CN.md) —— 项目总览与精简的"全平台构建"章节。
- [architecture.md](architecture.md) —— 分层总览、运行时核心、UI 外壳。
- [toolchain.md](toolchain.md) —— 员工、场景、工具链与质量门禁。
- [plugins.md](plugins.md) —— 插件文件位置与打包。
- [build.md](build.md) —— 本页英文版。

## 关键结论

- **TUI**：`CGO_ENABLED=0`，纯 Go，可交叉编译到 §2.1 的所有目标——**切勿**加 `-H windowsgui`。
- **GUI**：macOS/Linux 用 `CGO_ENABLED=1` + `-tags production`；Windows 用 `CGO_ENABLED=0` 即可（纯 Go COM 绑定，无需 mingw）。Windows 必须带 `-H windowsgui`（用 `scripts/pecheck` 校验，Subsystem=2）；macOS/Linux 宿主构建**不要**加它。
- **macOS 15+ 的 `setShowsBaselineSeparator:` 弃用告警无害**（Wails v2 固有，非错误），可用 `CGO_CFLAGS="-Wno-deprecated-declarations"` 屏蔽。
- **把 `contextloom` 与 `contextloom-gui` 放在同一目录**（或设 `CONTEXTLOOM_BIN`）。
- **headless Linux** 自动回退浏览器模式。
