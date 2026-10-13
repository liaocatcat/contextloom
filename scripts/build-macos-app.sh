#!/usr/bin/env bash
# =============================================================================
# ContextLoom macOS .app 打包脚本
#
# 背景：macOS 上 Finder 双击【裸二进制】会被 LaunchServices 用 Terminal.app
#       打开（弹出命令行窗口）。打包成 .app 应用包后，Finder 双击以 GUI
#       应用方式启动（无终端窗口），与 业界主流桌面编码 Agent / 常规桌面应用一致。
#
# 产物：./ContextLoom.app（仓库根，与裸二进制产物同目录）
#   Contents/
#     Info.plist          # 应用元数据（CFBundleExecutable=contextloom-gui 等）
#     MacOS/
#       contextloom-gui       # GUI 启动器（Wails 桌面窗口）
#       contextloom           # 主程序（GUI 启动时自动拉起 --serve 后端，同目录查找）
#
# 用法：
#   ./scripts/build-macos-app.sh          # 本机构建（Apple Silicon / Intel 各自）
#   make build-gui-macos                  # 等价
# =============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

APP_NAME="ContextLoom"
DIST="$ROOT"
APP="$DIST/$APP_NAME.app"
CONTENTS="$APP/Contents"
MACOS="$CONTENTS/MacOS"

echo "==> [1/4] 构建 contextloom 主程序（backend）"
go build -ldflags="-s -w" -o "$MACOS/contextloom" ./cmd/contextloom

echo "==> [2/4] 构建 contextloom-gui（Wails 桌面壳，-tags production）"
# CGO_CFLAGS 关闭 macOS 15+ 下 Wails v2 调用已弃用 setShowsBaselineSeparator:
# 产生的 -Wdeprecated-declarations 告警（是 warning 非 error，仅为消除噪声）。
CGO_ENABLED=1 CGO_CFLAGS="-Wno-deprecated-declarations" go build -tags production -ldflags="-s -w" -o "$MACOS/contextloom-gui" ./cmd/contextloom-gui

echo "==> [3/4] 生成 Info.plist"
mkdir -p "$CONTENTS"
cat > "$CONTENTS/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>ContextLoom</string>
  <key>CFBundleDisplayName</key>
  <string>ContextLoom 智能编码助手</string>
  <key>CFBundleIdentifier</key>
  <string>com.contextloom.agent</string>
  <key>CFBundleExecutable</key>
  <string>contextloom-gui</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>2.4.0</string>
  <key>CFBundleVersion</key>
  <string>2.4.0</string>
  <key>LSMinimumSystemVersion</key>
  <string>11.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>NSPrincipalClass</key>
  <string>NSApplication</string>
  <!-- 不隐藏 Dock 图标（LSUIElement 不设置=正常应用）：
       若需要纯托盘/无 Dock 图标应用，取消下一行注释 -->
  <!-- <key>LSUIElement</key><true/> -->
</dict>
</plist>
PLIST

echo "==> [4/4] 权限与校验"
chmod +x "$MACOS/contextloom" "$MACOS/contextloom-gui"
echo "--- 产物结构 ---"
find "$APP" -type f | sed "s|$ROOT/||"
echo "--- 文件类型校验（应为 Mach-O 64-bit executable） ---"
file "$MACOS/contextloom" "$MACOS/contextloom-gui"
echo "--- 二进制大小 ---"
du -sh "$MACOS/contextloom" "$MACOS/contextloom-gui" | sed "s|$MACOS/||"

echo ""
echo "✅ 打包完成：$APP"
echo "   使用：Finder 双击 ContextLoom.app 即可（无命令行窗口）；"
echo "   或命令行：open \"$APP\""
