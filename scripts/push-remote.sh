#!/usr/bin/env bash
# 一键建仓+推送脚本（令牌由环境变量传入，不落盘、不进 git）
# 用法:
#   GITEE_TOKEN=你的私人令牌 ./scripts/push-remote.sh gitee
#   GITHUB_TOKEN=你的私人令牌 ./scripts/push-remote.sh github
set -euo pipefail
cd "$(dirname "$0")/.."

platform="${1:?用法: $0 gitee|github}"
repo="contextloom"

case "$platform" in
  gitee)
    : "${GITEE_TOKEN:?请设置 GITEE_TOKEN 环境变量}"
    echo "[1/2] 在 Gitee 创建私有仓库 $repo ..."
    code=$(curl -s -o /tmp/gitee-create.json -w "%{http_code}" \
      "https://gitee.com/api/v5/user/repos" \
      -d "access_token=$GITEE_TOKEN" -d "name=$repo" \
      -d "private=true" -d "description=ContextLoom - a coding-agent harness in Go (TUI+GUI)")
    cat /tmp/gitee-create.json | head -c 300; echo
    if [ "$code" != "201" ] && ! grep -q "namespace" /tmp/gitee-create.json; then
      echo "建仓失败 (HTTP $code)，请检查令牌与权限(scope 需含 projects/user_info)"; exit 1
    fi
    user=$(curl -s "https://gitee.com/api/v5/user?access_token=$GITEE_TOKEN" | sed -n 's/.*"login":"\([^"]*\)".*/\1/p')
    git remote remove gitee 2>/dev/null || true
    git remote add gitee "https://oauth2:${GITEE_TOKEN}@gitee.com/${user}/${repo}.git"
    git push -u gitee main
    git remote set-url gitee "https://gitee.com/${user}/${repo}.git"
    echo "完成: https://gitee.com/${user}/${repo}"
    ;;
  github)
    : "${GITHUB_TOKEN:?请设置 GITHUB_TOKEN 环境变量}"
    echo "[1/2] 在 GitHub 创建私有仓库 $repo ..."
    code=$(curl -s -o /tmp/gh-create.json -w "%{http_code}" \
      -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/vnd.github+json" \
      "https://api.github.com/user/repos" \
      -d "{\"name\":\"$repo\",\"private\":true,\"description\":\"ContextLoom - a coding-agent harness in Go (TUI+GUI)\"}")
    cat /tmp/gh-create.json | head -c 300; echo
    if [ "$code" != "201" ] && ! grep -q "full_name" /tmp/gh-create.json; then
      echo "建仓失败 (HTTP $code)，请检查令牌与权限(repo/Administration:write)"; exit 1
    fi
    user=$(curl -s -H "Authorization: Bearer $GITHUB_TOKEN" https://api.github.com/user | sed -n 's/.*"login":"\([^"]*\)".*/\1/p')
    git remote remove github 2>/dev/null || true
    git remote add github "https://x-access-token:${GITHUB_TOKEN}@github.com/${user}/${repo}.git"
    git push -u github main
    git remote set-url github "https://github.com/${user}/${repo}.git"
    echo "完成: https://github.com/${user}/${repo}"
    ;;
  *)
    echo "未知平台: $platform（支持 gitee / github）"; exit 1 ;;
esac
