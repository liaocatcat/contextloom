.PHONY: build run test lint clean build-gui build-gui-win verify-gui

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

build-gui:
	CGO_ENABLED=1 $(GO) build -tags production -ldflags="-s -w $(GUI_LDFLAGS)" -o contextloom-gui ./cmd/contextloom-gui
	$(GO) run ./scripts/pecheck contextloom-gui.exe

# Windows GUI 交叉编译（macOS/Linux 主机）：必须带 -H windowsgui
# （否则 Subsystem=3 CONSOLE，双击弹黑窗——历史教训，勿删该标志）
build-gui-win:
	CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc GOOS=windows GOARCH=amd64 \
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
