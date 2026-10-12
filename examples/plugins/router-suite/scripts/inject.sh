#!/bin/sh
# inject.sh —— message 钩子脚本：每轮近距离引导（对标 dsh-routing-suite preset）
#
# 契约（见 internal/plugin/protocol.go MessageHook / runtime.go runPluginMessageHooks）：
#   stdin  = JSON {input, round, mode}
#   stdout = 引导段（原样注入下一轮请求、随用户消息之后）；空 = 不注入
#
# 单任务三锚（dsh preset 核心机制，每轮注入防稀释）：
#   ① 目标锚   —— 始终围绕任务目标，不扩散需求
#   ② 收敛锚   —— 本步只做一件事，产出可验证结果
#   ③ 反跑题锚 —— 偏离目标立即拉回
# round=1 注入完整三锚（任务启动）；round>1 注入收敛推进引导（反稀释）。
exec python3 -c '
import json, sys

d = {}
try:
    d = json.load(sys.stdin)
except Exception:
    pass

try:
    r = int(d.get("round", 1) or 1)
except Exception:
    r = 1

if r <= 1:
    print("【锚定引导 · 第1轮】三锚约束，全程遵守：")
    print("① 目标锚：始终围绕任务目标推进，不扩散需求、不节外生枝；")
    print("② 收敛锚：当前步骤只做一件事，产出可验证的结果；")
    print("③ 反跑题锚：一旦发现偏离目标，立即拉回主线再继续。")
else:
    print("【锚定引导 · 第%d轮】收敛推进：聚焦当前步骤，勿重复已完成的工作；若已偏离任务目标立即拉回。" % r)
'
