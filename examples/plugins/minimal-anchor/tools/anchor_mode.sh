#!/usr/bin/env bash
# dev_anchor_mode 工具：查询/切换锚定行为带
# stdin: JSON {"mode":"query|set","value":"auto|standard|full"}
# stdout: 操作结果
set -euo pipefail
DIR="$(cd "$(dirname "$(dirname "$0")")" && pwd)"
STATE_FILE="$DIR/.anchor_state"

INPUT="$(cat 2>/dev/null || true)"

# 解析 JSON
CMD="query"
VAL=""
if command -v python3 >/dev/null 2>&1; then
  CMD="$(printf '%s' "$INPUT" | python3 -c 'import sys,json
try:
  d=json.load(sys.stdin)
  print(d.get("mode","query"))
except Exception:
  print("query")' 2>/dev/null || echo query)"
  VAL="$(printf '%s' "$INPUT" | python3 -c 'import sys,json
try:
  d=json.load(sys.stdin)
  print(d.get("value","") or "")
except Exception:
  print("")' 2>/dev/null || echo "")"
fi

case "$CMD" in
  set)
    if [ -z "$VAL" ]; then
      echo '{"error":"value is required for set"}'
      exit 0
    fi
    case "$VAL" in
      auto|standard|full)
        printf '%s' "$VAL" > "$STATE_FILE"
        echo "{\"ok\":true,\"mode\":\"${VAL}\",\"message\":\"锚定模式已切换为: ${VAL}\"}"
        ;;
      *)
        echo "{\"error\":\"invalid value: ${VAL}, expected auto|standard|full\"}"
        exit 0
        ;;
    esac
    ;;
  *)
    MODE="auto"
    if [ -f "$STATE_FILE" ]; then
      MODE="$(cat "$STATE_FILE" 2>/dev/null | tr -d '[:space:]' || echo auto)"
    fi
    echo "{\"mode\":\"${MODE}\",\"message\":\"当前锚定模式: ${MODE}\"}"
    ;;
esac