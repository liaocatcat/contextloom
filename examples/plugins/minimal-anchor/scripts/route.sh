#!/usr/bin/env bash
# route 钩子：极简锚定路由决策
# stdin: JSON {"mode":"chat|task|goal","input":"任务描述","round":N}
# stdout: JSON {"prompt":"...","tool_scope":"core"} 或纯文本，或空（不注入）
#
# 决策逻辑（对标 dsh 极简模式"先窄后宽"）：
#   - 行为带 standard/full：第 1 轮也空输出（等同无插件）
#   - 行为带 auto（默认）：
#       round == 1 → {"prompt":"极简 persona","tool_scope":"core"}（锁定）
#       round >  1 → 空（锚定后释放，不注入避免稀释）

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
STATE_FILE="$DIR/../.anchor_state"

# 读取 stdin JSON；失败时按第 1 轮处理（保守锁定）
INPUT="$(cat 2>/dev/null || true)"
ROUND=""
if command -v python3 >/dev/null 2>&1; then
  ROUND="$(printf '%s' "$INPUT" | python3 -c 'import sys,json
try:
  d=json.load(sys.stdin)
  print(str(d.get("round",1)))
except Exception:
  print("1")' 2>/dev/null || echo 1)"
else
  ROUND="$(printf '%s' "$INPUT" | grep -o '"round":[0-9]*' | head -1 | grep -o '[0-9]*' || echo 1)"
fi
[ -z "$ROUND" ] && ROUND=1

# 行为带：默认 auto；standard/full 时不锚定
MODE="auto"
if [ -f "$STATE_FILE" ]; then
  MODE="$(cat "$STATE_FILE" 2>/dev/null | tr -d '[:space:]' || echo auto)"
fi

case "$MODE" in
  standard|full)
    :  # 不注入（等同无插件）
    ;;
  *)
    if [ "$ROUND" = "1" ]; then
      # 首轮极简锚定：persona 走训练分布 + 工具收敛到核心
      echo '{"prompt":"【锚定引导 · 简约模式】你是 ContextLoom 编码助手。聚焦当前任务，逐步完成，输出简洁直接。","tool_scope":"core"}'
    else
      :  # 锚定后释放：不输出任何内容，全部工具可用
    fi
    ;;
esac