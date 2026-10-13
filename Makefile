.PHONY: build run test lint clean build-all build-gui build-gui-win verify-gui

BINARY := contextloom
GO := go
GOFLAGS := -ldflags="-s -w"
# Windows 下 Go 默认构建为 console 子系统：双击 contextloom-gui.exe 会弹出
# 黑色命令行窗口（历史 bug：用户实测"GUI 无法隐藏 cmd 黑窗"）。
# 修复：加 -H windowsgui 改为 GUI 子系统（无控制台、静默启动）。
#
# 关键教训：-H windowsgui 是 Go linker 按【目标 GOOS=windows】生效的标志，
# 与构建主机无关——此前用 ifeq ($(OS),Windows_NT) 按 make 宿主判断，导致
# 交叉编译（Linux/macOS 上产出 windows exe）或直接 go build 时该标志丢失，
# 产出的 contextloom-gui.exe subsystem=3(CONSOLE)，双击必弹黑窗。
# build-gui 目标专用于 Windows GUI，必须无条件携带该标志。
#
# 防覆盖：build-gui / build-gui-win 构建后自动跑 scripts/pecheck 校验
# PE Subsystem=2（GUI），漏标或产物被旧命令覆盖时立即报错（历史回归根因）。
GUI_LDFLAGS := -H windowsgui

# 宿主 GOOS：让 build-gui 感知【构建主机】平台（注意与上面的【目标 GOOS】区分）。
HOST_GOOS := $(shell go env GOOS)

# build-gui 的宿主相关链接标志：
#  - HOST_GOOS=windows：与 build-gui-win 一致，必须带 -H windowsgui。
#  - HOST_GOOS=darwin/linux：绝对不能带 -H windowsgui！
#    原因：-H windowsgui 是链接器按【目标 GOOS=windows】生效的标志；在非 Windows
#    宿主上它会被 darwin/linux 的 Go linker 当作非法参数，链接阶段硬失败
#    （报 libc_fdopendir: initialize bounds (0 < 8) ... link: too many errors，
#    exit 1），导致 `make build-gui` 在 macOS 上直接退出 2。
#    Windows 交叉编译路径由 build-gui-win 单独负责（始终用 GUI_LDFLAGS）。
ifeq ($(HOST_GOOS),windows)
GUI_BUILD_LDFLAGS := -H windowsgui
else
GUI_BUILD_LDFLAGS :=
endif

# macOS 宿主下 Wails v2 的 internal/frontend/desktop/darwin/WailsContext.m 调用
# 了 macOS 15+ 已弃用的 setShowsBaselineSeparator:，导致每次 GUI 构建都打印
# -Wdeprecated-declarations 告警（注意：是 warning 非 error，构建仍成功，但噪声
# 会误导用户以为是构建失败）。所有 Wails v2 版本（含 v2.16.0）都仍调用该 API，
# 升级无法消除。已验证 CGO_CFLAGS="-Wno-deprecated-declarations" 可完全静默该告警
# 且构建照常成功；仅在 darwin 宿主生效，其他平台无副作用。
ifeq ($(HOST_GOOS),darwin)
export CGO_CFLAGS := -Wno-deprecated-declarations
endif

build:
	$(GO) build $(GOFLAGS) -o $(BINARY) ./cmd/contextloom

run:
	$(GO) run ./cmd/contextloom

test:
	$(GO) test -race -cover ./...

lint:
	golangci-lint run ./...

clean:
	rm -f $(BINARY)

# 全平台 TUI 交叉编译（纯 Go，CGO_ENABLED=0，无需任何额外工具链）。
# 产物统一输出到当前目录，命名 contextloom-<os>-<arch>[.exe]，
# 与 docs/build.md 的矩阵保持一致（arm 用 GOARM 区分 v5/v6/v7）。
#
# 历史 bug：早期用 `[ -n "$arm" ] && CGO_ENABLED=0 ... GOARM="$arm" go build`
# 的写法，`VAR=val cmd` 前缀在 `&&` 链中会被 shell 当作独立命令解析，
# 报 "GOARM=7: command not found"，导致全部 linux/arm 目标构建失败。
# 现改为在子 shell 内 export，避免该展开陷阱。
build-all:
	@set -e; \
	targets='darwin/amd64 darwin/arm64 \
	  linux/amd64 linux/arm64 linux/386 \
	  linux/arm/5 linux/arm/6 linux/arm/7 \
	  linux/ppc64le linux/s390x linux/riscv64 linux/loong64 \
	  linux/mips64le linux/mipsle \
	  windows/amd64 windows/arm64 windows/386 \
	  freebsd/amd64 freebsd/arm64 \
	  netbsd/amd64 openbsd/amd64 dragonfly/amd64 \
	  solaris/amd64 aix/ppc64'; \
	for t in $$targets; do \
	  os=$${t%%/*}; rest=$${t#*/}; arch=$${rest%%/*}; arm=""; \
	  case "$$rest" in */*) arm=$${rest#*/};; esac; \
	  out="contextloom-$$os-$$arch"; \
	  if [ "$$arch" = "arm" ] && [ -n "$$arm" ]; then out="contextloom-$$os-armv$$arm"; fi; \
	  if [ "$$os" = "windows" ]; then out="$$out.exe"; fi; \
	  echo "==> $$os/$$arch$${arm:+/GOARM=$$arm} -> $$out"; \
	  ( export CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch; \
	    [ -n "$$arm" ] && export GOARM=$$arm; \
	    $(GO) build $(GOFLAGS) -o "$$out" ./cmd/contextloom ); \
	done; \
	echo "✅ 全平台 TUI 构建完成"

# 宿主感知的 GUI 构建：Windows 宿主产出 GUI 子系统 exe 并跑 pecheck 守护；
# darwin/linux 宿主不加 -H windowsgui（否则链接器硬失败，见上），并跳过 pecheck
# （非 Windows 产物是 Mach-O/ELF，不是 PE，pecheck 无从校验）。
build-gui:
	CGO_ENABLED=1 $(GO) build -tags production -ldflags="-s -w $(GUI_BUILD_LDFLAGS)" -o contextloom-gui ./cmd/contextloom-gui
ifeq ($(HOST_GOOS),windows)
	$(GO) run ./scripts/pecheck contextloom-gui.exe
else
	@echo "pecheck skipped (host=$(HOST_GOOS); not a PE target)"
endif

# Windows GUI 交叉编译（任意主机）：必须带 -H windowsgui
# （否则 Subsystem=3 CONSOLE，双击弹黑窗——历史教训，勿删该标志）。
#
# 更正历史文档错误：Windows GUI **不需要 CGO / mingw-w64**。Wails v2 的 Windows
# 后端是纯 Go 的 COM 绑定（wailsapp/go-webview2，模块内 `import "C"` 仅出现在
# *_darwin.go），故 CGO_ENABLED=0 即可交叉编译——实测传 bogus CC 也构建成功，
# 印证该路径根本不调用 C 编译器。此前 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc
# 的要求是误传，会让没有 mingw 的机器白白放弃 Windows GUI 产物。
build-gui-win:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		$(GO) build -tags production -ldflags="-s -w $(GUI_LDFLAGS)" -o contextloom-gui-windows-amd64.exe ./cmd/contextloom-gui
	$(GO) run ./scripts/pecheck contextloom-gui-windows-amd64.exe

# 校验已产出的 contextloom-gui 为 GUI 子系统（Subsystem=2），
# 防任何直接 go build / 旧命令覆盖产物为 CONSOLE(3) 导致双击弹黑窗。
verify-gui:
	$(GO) run ./scripts/pecheck contextloom-gui.exe

# macOS GUI .app 打包：Finder 双击以 GUI 应用启动（无命令行窗口）。
# 背景：macOS 双击裸二进制会被 LaunchServices 用 Terminal.app 打开（弹窗），
#       打包 .app 后以 GUI 应用方式启动，对标桌面应用标准行为。
build-gui-macos:
	./scripts/build-macos-app.sh
