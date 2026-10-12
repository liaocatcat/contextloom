#!/usr/bin/env bash
# message 钩子：单任务三锚近距离引导（对标 dsh "近距离引导/反稀释"）
# stdin: JSON {"input":"任务描述","round":N}
# stdout: 注入文本（user role，随用户消息之后）或空（不注入）
#
#   round == 1 → 三锚完整引导（目标锚/收敛锚/反跑题锚）
#   round >  1 → 收敛推进轻引导（反稀释：不再刷三锚，只提醒收尾）
#   round >  2 → 空（零注入，避免干扰）

set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
STATE_FILE="$DIR/../.anchor_state"

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

MODE="auto"
if [ -f "$STATE_FILE" ]; then
  MODE="$(cat "$STATE_FILE" 2>/dev/null | tr -d '[:space:]' || echo auto)"
fi

case "$MODE" in
  standard|full)
    : ;;
  *)
    case "$ROUND" in
      1)
        cat <<'EOF'
【锚定引导 · 三锚约束】本次任务全程遵守：
① 目标锚：始终围绕任务目标推进，不扩散需求、不节外生枝；
② 收敛锚：当前步骤只做一件事，产出可验证的结果；
③ 反跑题锚：一旦发现偏离目标，立即拉回主线再继续。
EOF
        ;;
      2)
        cat <<'EOF'
【收敛推进】请检查当前进展：已完成部分是否有可验证产出？若可以，请收尾总结；若仍需要操作，请先完成最关键的一步。
EOF
        ;;
      *)
        : ;;
    esac
    ;;
esac