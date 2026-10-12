#!/usr/bin/env bash
# dev_anchor_status 工具：查询 minimal-anchor 插件锚定状态
# stdin: JSON {"status":"running|stopped"}
# stdout: 锚定状态信息（JSON）
set -euo pipefail
DIR="$(cd "$(dirname "$(dirname "$0")")" && pwd)"
STATE_FILE="$DIR/.anchor_state"

MODE="auto"
if [ -f "$STATE_FILE" ]; then
  MODE="$(cat "$STATE_FILE" 2>/dev/null | tr -d '[:space:]' || echo auto)"
fi

cat <<EOF
{
  "status": "running",
  "mode": "${MODE}",
  "version": "0.1.0",
  "description": "minimal-anchor 极简锚定路由引擎"
}
EOF