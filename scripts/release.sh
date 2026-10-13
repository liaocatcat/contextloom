#!/usr/bin/env bash
# =============================================================================
# ContextLoom 一键发布脚本：构建全平台产物 → 校验 → 打 tag → 推送 →
# 上传到 GitHub 与 Gitee 两个仓库的 Releases。
#
# 用法：
#   ./scripts/release.sh v2.4.0              # 构建+校验+打tag+推送（不建 Release）
#   ./scripts/release.sh v2.4.0 --publish    # 额外创建两个仓库的 Release 并上传附件
#
# 令牌（仅 --publish 需要，经环境变量传入，不落盘、不进 git）：
#   GITHUB_TOKEN  需要 repo 权限（或 public_repo）
#   GITEE_TOKEN   需要 projects 权限
#
# 说明：产物统一输出到仓库根目录（与项目既有约定一致，已由 .gitignore 的
#       /contextloom-* 规则忽略），绝不进入 git 仓库——Releases 附件才是分发通道。
# =============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

TAG="${1:-}"
if [ -z "$TAG" ]; then
  echo "用法: $0 <tag> [--publish]   例如: $0 v2.4.0 --publish" >&2
  exit 2
fi
PUBLISH=0
[ "${2:-}" = "--publish" ] && PUBLISH=1

OUT="$ROOT"   # 产物直接落仓库根目录（项目既有约定，勿改回 dist/）
GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || GO=/opt/homebrew/bin/go

# 与 pkg/version/version.go 对齐校验，防止 tag 与代码版本漂移
CODE_VER="$("$GO" run ./cmd/contextloom --version 2>/dev/null | sed -n 's/.*v\([0-9][0-9.]*\).*/\1/p' | head -1)"
[ "v$CODE_VER" = "$TAG" ] || { echo "❌ tag($TAG) 与代码版本(v$CODE_VER) 不一致，请先同步 pkg/version/version.go" >&2; exit 1; }

# 只清理历史产物文件——OUT 是仓库根目录，绝不能 rm -rf "$OUT"
rm -f "$OUT"/contextloom-* "$OUT"/SHA256SUMS 2>/dev/null || true

echo "==> [1/3] 构建全平台 TUI（24 目标，纯 Go）"
targets=(
  darwin/amd64 darwin/arm64
  linux/amd64 linux/arm64 linux/386
  linux/arm/5 linux/arm/6 linux/arm/7
  linux/ppc64le linux/s390x linux/riscv64 linux/loong64
  linux/mips64le linux/mipsle
  windows/amd64 windows/arm64 windows/386
  freebsd/amd64 freebsd/arm64
  netbsd/amd64 openbsd/amd64 dragonfly/amd64
  solaris/amd64 aix/ppc64
)
for t in "${targets[@]}"; do
  os="${t%%/*}"; rest="${t#*/}"; arch="${rest%%/*}"; arm=""
  case "$rest" in */*) arm="${rest#*/}";; esac
  out="contextloom-$os-$arch"
  [ "$arch" = "arm" ] && [ -n "$arm" ] && out="contextloom-$os-armv$arm"
  [ "$os" = "windows" ] && out="$out.exe"
  printf "    %-18s -> %s\n" "$os/$arch${arm:+/$arm}" "$out"
  ( export CGO_ENABLED=0 GOOS="$os" GOARCH="$arch"; [ -n "$arm" ] && export GOARM="$arm"
    "$GO" build -ldflags="-s -w" -o "$OUT/$out" ./cmd/contextloom )
done

echo "==> [2/3] 构建 GUI（macOS arm64/amd64、Windows amd64/arm64）"
# macOS：需要 CGO + clang（Wails 的 darwin 前端是 Objective-C）。
# CGO_CFLAGS 屏蔽 macOS 15+ Wails 弃用告警（warning 非 error，构建仍 exit 0）。
if command -v clang >/dev/null 2>&1; then
  CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CGO_CFLAGS="-Wno-deprecated-declarations" \
    "$GO" build -tags production -ldflags="-s -w" -o "$OUT/contextloom-gui-darwin-arm64" ./cmd/contextloom-gui
  CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="clang -arch x86_64" CGO_CFLAGS="-Wno-deprecated-declarations" \
    "$GO" build -tags production -ldflags="-s -w" -o "$OUT/contextloom-gui-darwin-amd64" ./cmd/contextloom-gui
else
  echo "    ⚠️  跳过 macOS GUI（未找到 clang）"
fi

# Windows GUI：**不需要 CGO / mingw-w64**。
# 更正历史文档错误：Wails v2 的 Windows 后端是纯 Go 的 COM 绑定
# （wailsapp/go-webview2，无 cgo——模块内 `import "C"` 仅出现在 *_darwin.go），
# 因此 CGO_ENABLED=0 即可交叉编译，任何宿主都能产出。实测传 bogus CC 也构建成功，
# 印证该路径根本不调用 C 编译器。
# -H windowsgui 必需（否则 PE Subsystem=3 CONSOLE，双击弹黑窗）——构建后 pecheck 守护。
for arch in amd64 arm64; do
  out="contextloom-gui-windows-$arch.exe"
  CGO_ENABLED=0 GOOS=windows GOARCH="$arch" \
    "$GO" build -tags production -ldflags="-s -w -H windowsgui" -o "$OUT/$out" ./cmd/contextloom-gui
  "$GO" run ./scripts/pecheck "$OUT/$out"
done

echo "==> [3/3] 生成校验和"
( cd "$OUT" && shasum -a 256 contextloom-* > SHA256SUMS 2>/dev/null || sha256sum contextloom-* > SHA256SUMS )
echo "    产物数: $(ls -1 "$OUT"/contextloom-* 2>/dev/null | wc -l | tr -d ' ')  位置: $OUT"

if [ "$PUBLISH" -eq 0 ]; then
  echo "✅ 构建完成（未发布）。打 tag 并推送："
  echo "   git tag -a $TAG -m \"$TAG\" && git push github $TAG && git push gitee $TAG"
  echo "   发布 Releases：$0 $TAG --publish"
  exit 0
fi

# ---------- 打 tag + 推送 ----------
if ! git rev-parse "$TAG" >/dev/null 2>&1; then
  git tag -a "$TAG" -m "$TAG"
  echo "==> 已创建 tag $TAG"
fi
git push github "$TAG" || true
git push gitee  "$TAG" || true

NOTES="$ROOT/docs/release/$TAG.md"
[ -f "$NOTES" ] || NOTES="$ROOT/docs/release/v${CODE_VER}.md"
[ -f "$NOTES" ] || { echo "❌ 缺少 Release 说明文件: docs/release/$TAG.md" >&2; exit 1; }

# ---------- GitHub Release ----------
if [ -n "${GITHUB_TOKEN:-}" ]; then
  echo "==> 创建 GitHub Release 并上传附件"
  # 注意：GitHub 返回 "login": "xxx"（冒号后有空格），正则必须容忍空白，否则解析为空 -> 404
  GH_USER=$(curl -s -H "Authorization: Bearer $GITHUB_TOKEN" https://api.github.com/user | sed -n 's/.*"login"[: ]*"\([^"]*\)".*/\1/p')
  [ -n "$GH_USER" ] || { echo "❌ 无法解析 GitHub 用户名"; exit 1; }
  # 用 python 生成合法 JSON（正确转义 Release 说明里的换行/引号），写入临时文件避免命令行长度与转义问题
  python3 -c 'import json,sys;print(json.dumps({"tag_name":sys.argv[1],"name":sys.argv[1],"body":open(sys.argv[2]).read()}))' "$TAG" "$NOTES" > /tmp/gh-release-body.json
  curl -s -X POST \
    -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/vnd.github+json" \
    "https://api.github.com/repos/${GH_USER}/ds-contextloom-harness/releases" \
    --data @/tmp/gh-release-body.json \
    -o /tmp/gh-release.json
  GH_UPLOAD=$(sed -n 's/.*"upload_url": "\([^"]*\){.*/\1/p' /tmp/gh-release.json)
  if [ -z "$GH_UPLOAD" ]; then echo "❌ GitHub Release 创建失败:"; head -c 400 /tmp/gh-release.json; echo; else
    for f in "$OUT"/contextloom-* "$OUT"/SHA256SUMS; do
      [ -f "$f" ] || continue
      curl -s -X POST -H "Authorization: Bearer $GITHUB_TOKEN" -H "Content-Type: application/octet-stream" \
        --data-binary "@$f" "${GH_UPLOAD}?name=$(basename "$f")" -o /dev/null
      echo "    ↑ $(basename "$f")"
    done
    echo "    ✅ https://github.com/${GH_USER}/ds-contextloom-harness/releases/tag/$TAG"
  fi
else
  echo "⚠️  未设置 GITHUB_TOKEN，跳过 GitHub Release"
fi

# ---------- Gitee Release ----------
if [ -n "${GITEE_TOKEN:-}" ]; then
  echo "==> 创建 Gitee Release 并上传附件"
  GITEE_USER=$(curl -s "https://gitee.com/api/v5/user?access_token=$GITEE_TOKEN" | sed -n 's/.*"login"[: ]*"\([^"]*\)".*/\1/p')
  [ -n "$GITEE_USER" ] || { echo "❌ 无法解析 Gitee 用户名"; exit 1; }
  # 先建一次 release（不带附件），拿到 release id —— 否则逐个文件建 release 会产生多个重复 Release
  # 注意：body 用 `=<file`（读取文件内容作为字段值），而非 `=@file`（那是上传文件附件，会被判 body is invalid）
  curl -s -X POST "https://gitee.com/api/v5/repos/${GITEE_USER}/ds-contextloom-harness/releases" \
    -F "access_token=$GITEE_TOKEN" -F "tag_name=$TAG" -F "name=$TAG" \
    -F "body=<$NOTES" -F "target_commitish=main" -o /tmp/gitee-release.json
  GITEE_RID=$(sed -n 's/.*"id":\([0-9]*\).*/\1/p' /tmp/gitee-release.json | head -1)
  if [ -z "$GITEE_RID" ]; then echo "❌ Gitee Release 创建失败:"; head -c 400 /tmp/gitee-release.json; echo; else
    for f in "$OUT"/contextloom-* "$OUT"/SHA256SUMS; do
      [ -f "$f" ] || continue
      curl -s -X POST "https://gitee.com/api/v5/repos/${GITEE_USER}/ds-contextloom-harness/releases/${GITEE_RID}/attach_files" \
        -F "access_token=$GITEE_TOKEN" -F "file=@$f" -o /dev/null || true
      echo "    ↑ $(basename "$f")"
    done
    echo "    ✅ https://gitee.com/${GITEE_USER}/ds-contextloom-harness/releases"
  fi
else
  echo "⚠️  未设置 GITEE_TOKEN，跳过 Gitee Release"
fi

echo "🎉 发布流程结束"
