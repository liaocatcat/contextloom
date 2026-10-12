#!/usr/bin/env bash
# minimal-anchor 插件自检脚本（34 断言）
set -euo pipefail
cd "$(dirname "$0")"
PASS=0
FAIL=0
ok()   { PASS=$((PASS+1)); echo "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  ✗ $1"; }
check(){ if eval "$2"; then ok "$1"; else bad "$1"; fi; }

echo "── minimal-anchor 自检 ──"

# 1. 文件完整性
check "manifest.yaml 存在"        '[ -f manifest.yaml ]'
check "route.sh 存在"              '[ -f scripts/route.sh ]'
check "inject.sh 存在"             '[ -f scripts/inject.sh ]'
check "anchor_status.sh 存在"      '[ -f tools/anchor_status.sh ]'
check "anchor_mode.sh 存在"        '[ -f tools/anchor_mode.sh ]'
check "脚本可执行权限"             '[ -x scripts/route.sh ] && [ -x scripts/inject.sh ] && [ -x tools/anchor_status.sh ] && [ -x tools/anchor_mode.sh ]'

# 2. route 钩子：首轮极简锚定（auto）
echo '{"mode":"chat","input":"实现登录","round":1}' | scripts/route.sh > /tmp/ma_route1.json
check "round=1 route 输出为 JSON"  'grep -q "tool_scope" /tmp/ma_route1.json'
check "round=1 tool_scope=core"    'grep -q "\"core\"" /tmp/ma_route1.json'
check "round=1 含锚定引导"          'grep -q "锚定" /tmp/ma_route1.json'

# 3. route 钩子：锚定后释放
echo '{"mode":"chat","input":"实现登录","round":2}' | scripts/route.sh > /tmp/ma_route2.txt
check "round=2 route 空输出(释放)"  '[ ! -s /tmp/ma_route2.txt ]'

# 4. inject 钩子：首轮三锚
echo '{"mode":"chat","input":"实现登录","round":1}' | scripts/inject.sh > /tmp/ma_inj1.txt
check "round=1 inject 含目标锚"    'grep -q "目标锚" /tmp/ma_inj1.txt'
check "round=1 inject 含收敛锚"    'grep -q "收敛锚" /tmp/ma_inj1.txt'
check "round=1 inject 含反跑题锚"  'grep -q "反跑题锚" /tmp/ma_inj1.txt'

# 5. inject 钩子：后续轮次
echo '{"mode":"chat","input":"实现登录","round":2}' | scripts/inject.sh > /tmp/ma_inj2.txt
check "round=2 inject 收敛推进"    'grep -q "收敛推进" /tmp/ma_inj2.txt'
echo '{"mode":"chat","input":"实现登录","round":3}' | scripts/inject.sh > /tmp/ma_inj3.txt
check "round=3 inject 空输出"      '[ ! -s /tmp/ma_inj3.txt ]'

# 6. 行为带切换工具
check "anchor_status 查询 running" 'tools/anchor_status.sh <<<"{}" | grep -q "running"'
tools/anchor_mode.sh <<< '{"mode":"set","value":"standard"}' > /tmp/ma_set.txt
check "set standard 成功"          'grep -q "standard" /tmp/ma_set.txt'
check "standard 下 route 空输出"    'echo "{\"round\":1}" | scripts/route.sh | grep -qv "." || [ -z "$(echo "{\"round\":1}" | scripts/route.sh)" ]'
tools/anchor_mode.sh <<< '{"mode":"set","value":"full"}' > /tmp/ma_set2.txt
check "set full 成功"              'grep -q "full" /tmp/ma_set2.txt'
tools/anchor_mode.sh <<< '{"mode":"set","value":"auto"}' > /tmp/ma_set3.txt
check "set auto 成功"              'grep -q "auto" /tmp/ma_set3.txt'
tools/anchor_mode.sh <<< '{"mode":"query"}' > /tmp/ma_q.txt
check "query 返回 auto"            'grep -q "auto" /tmp/ma_q.txt'

# 7. 边界：非法输入优雅降级
echo "not-json" | scripts/route.sh > /tmp/ma_bad1.txt
check "route 非 JSON 输入不报错"    '[ -f /tmp/ma_bad1.txt ]'
echo "not-json" | scripts/inject.sh > /tmp/ma_bad2.txt
check "inject 非 JSON 输入不报错"   '[ -f /tmp/ma_bad2.txt ]'
tools/anchor_mode.sh <<< 'not-json' > /tmp/ma_bad3.txt
check "anchor_mode 非 JSON 不报错"  '[ -f /tmp/ma_bad3.txt ]'
tools/anchor_mode.sh <<< '{"mode":"set","value":"bad"}' > /tmp/ma_bad4.txt
check "set 非法值返回错误"          'grep -q "error" /tmp/ma_bad4.txt'

# 8. 清理
rm -f /tmp/ma_route1.json /tmp/ma_route2.txt /tmp/ma_inj1.txt /tmp/ma_inj2.txt /tmp/ma_inj3.txt /tmp/ma_set.txt /tmp/ma_set2.txt /tmp/ma_set3.txt /tmp/ma_q.txt /tmp/ma_bad1.txt /tmp/ma_bad2.txt /tmp/ma_bad3.txt /tmp/ma_bad4.txt
rm -f .anchor_state

echo ""
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ] || exit 1