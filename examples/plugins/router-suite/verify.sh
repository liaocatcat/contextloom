#!/bin/sh
# router-suite 插件验证脚本
# 验证：manifest 结构、脚本契约（route 三行为带 / message 三锚 / 工具 JSON 契约）
set -u
cd "$(dirname "$0")"
PASS=0
FAIL=0

check() {
  if [ "$1" = "0" ]; then
    PASS=$((PASS + 1))
    echo "PASS: $2"
  else
    FAIL=$((FAIL + 1))
    echo "FAIL: $2"
  fi
}

echo "=== 1. manifest 结构 ==="
[ -f manifest.yaml ] && grep -q 'name: router-suite' manifest.yaml; check $? "manifest.yaml 存在且 name=router-suite"
grep -q 'hooks:' manifest.yaml; check $? "含 hooks 节"
grep -q 'message:' manifest.yaml; check $? "含 hooks.message"
grep -q 'route:' manifest.yaml; check $? "含 hooks.route"
grep -q 'dev_router_status' manifest.yaml; check $? "含工具 dev_router_status"
grep -q 'dev_router_mode' manifest.yaml; check $? "含工具 dev_router_mode"

echo "=== 2. route.sh（route 钩子：三行为带路由）==="
R1=$(echo '{"mode":"task","input":"请设计一个登录模块的架构方案","round":1}' | scripts/route.sh 2>/dev/null)
R2=$(echo '{"mode":"task","input":"修复登录接口的 bug","round":1}' | scripts/route.sh 2>/dev/null)
R3=$(echo '{"mode":"task","input":"先设计再实现一个完整的下单流程","round":1}' | scripts/route.sh 2>/dev/null)
R4=$(echo '{"mode":"chat","input":"你好","round":1}' | scripts/route.sh 2>/dev/null)
[ -n "$R1" ] && echo "$R1" | grep -q 'spec 计划者'; check $? "含'规划/设计'关键词→spec计划者（got: $(echo "$R1" | head -1)）"
[ -n "$R2" ] && echo "$R2" | grep -q 'react 执行者'; check $? "含'修复'关键词→react执行者（got: $(echo "$R2" | head -1)）"
[ -n "$R3" ] && echo "$R3" | grep -q 'mixed 混合'; check $? "含'先设计再实现'→mixed混合（got: $(echo "$R3" | head -1)）"
[ -n "$R4" ] && echo "$R4" | grep -q 'mixed'; check $? "未知关键词+chat→mixed兜底（got: $(echo "$R4" | head -1)）"

echo "=== 3. inject.sh（message 钩子：三锚 + 收敛引导）==="
OUT1=$(echo '{"input":"实现登录","round":1,"mode":"task"}' | scripts/inject.sh 2>/dev/null)
OUT2=$(echo '{"input":"实现登录","round":2,"mode":"task"}' | scripts/inject.sh 2>/dev/null)
[ -n "$OUT1" ] && echo "$OUT1" | grep -q '目标锚'; check $? "round=1 注入目标锚（got: $(echo "$OUT1" | head -1)）"
[ -n "$OUT1" ] && echo "$OUT1" | grep -q '收敛锚'; check $? "round=1 注入收敛锚"
[ -n "$OUT1" ] && echo "$OUT1" | grep -q '反跑题锚'; check $? "round=1 注入反跑题锚"
[ -n "$OUT2" ] && echo "$OUT2" | grep -q '收敛推进'; check $? "round=2 输出收敛推进引导（got: $(echo "$OUT2" | head -1)）"
[ "$OUT1" != "$OUT2" ]; check $? "round 不同输出不同（动态性）"

echo "=== 4. router_status.sh（插件工具：JSON 输入输出）==="
T=$(echo '{"status":"check"}' | scripts/router_status.sh 2>/dev/null)
echo "$T" | grep -q '"status":"running"'; check $? "输出 JSON 含 status=running（got: ${T}）"
echo "$T" | grep -q '"plugin":"router-suite"'; check $? "输出 JSON 含 plugin 名"

echo "=== 5. router_mode.sh（AI 自优化工具：query/set）==="
M1=$(echo '{"mode":"query"}' | scripts/router_mode.sh 2>/dev/null)
echo "$M1" | grep -q '"ok": true'; check $? "query 返回 ok=true（got: ${M1}）"
echo "$M1" | grep -q '"mode"'; check $? "query 返回当前 mode 字段"
M2=$(echo '{"mode":"set","value":"spec"}' | scripts/router_mode.sh 2>/dev/null)
echo "$M2" | grep -q '"ok": true'; check $? "set 返回 ok=true（got: ${M2}）"
R5=$(echo '{"mode":"chat","input":"你好","round":1}' | scripts/route.sh 2>/dev/null)
echo "$R5" | grep -q 'spec 计划者'; check $? "set 覆盖后 route 优先采用 spec（got: $(echo "$R5" | head -1)）"
M3=$(echo '{"mode":"set","value":"invalid"}' | scripts/router_mode.sh 2>/dev/null)
echo "$M3" | grep -q '"ok": false'; check $? "非法 value 返回 ok=false（got: ${M3}）"
# 清理 set 测试写入的状态文件（避免污染后续自动分类）
rm -f .route_state

echo "=== 6. 边界与异常路径（防回归）==="
# 非 JSON stdin：route/inject 不崩溃，走兜底分支
E1=$(echo "not-json" | scripts/route.sh 2>/dev/null)
[ -n "$E1" ]; check $? "非JSON输入 route 不崩溃且输出兜底（got: $(echo "$E1" | head -1)）"
E2=$(echo "garbage" | scripts/inject.sh 2>/dev/null)
[ -n "$E2" ] && echo "$E2" | grep -q '目标锚'; check $? "非JSON输入 inject 回退第1轮三锚（got: $(echo "$E2" | head -1)）"
# 非法 round：回退第 1 轮
E3=$(echo '{"input":"x","round":"abc"}' | scripts/inject.sh 2>/dev/null)
[ -n "$E3" ] && echo "$E3" | grep -q '目标锚'; check $? "round=abc 回退第1轮三锚（got: $(echo "$E3" | head -1)）"
# 损坏状态文件：忽略损坏内容并回退 auto
echo "corrupted!!" > .route_state
E4=$(echo '{"mode":"query"}' | scripts/router_mode.sh 2>/dev/null)
echo "$E4" | grep -q '"mode": "auto"'; check $? "损坏状态文件 query 回退 auto（got: ${E4}）"
E5=$(echo '{"mode":"chat","input":"你好","round":1}' | scripts/route.sh 2>/dev/null)
echo "$E5" | grep -q 'mixed'; check $? "损坏状态文件 route 忽略覆盖回自动分类（got: $(echo "$E5" | head -1)）"
rm -f .route_state

echo ""
echo "=== 结果: $PASS PASS, $FAIL FAIL ==="
[ "$FAIL" = "0" ] && echo "ALL PASS" || echo "HAS FAILURES"
exit "$FAIL"
