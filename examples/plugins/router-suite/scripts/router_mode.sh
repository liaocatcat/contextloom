#!/bin/sh
# router_mode.sh —— dev_router_mode 工具实现脚本（AI 自优化路由工具）
#
# 契约：stdin 收到工具参数 JSON（{mode: query|set, value: spec|react|mixed|weak}），
#       stdout 输出 JSON 结果，原样作为工具结果返回给模型。
# 注意：stdout 只输出 JSON，不要混入日志（工具结果会被完整解析）。
#
# 功能（对标 dsh-routing-suite 的 dev_router_mode 自优化工具）：
#   query —— 查询当前路由模式（用户覆盖值或"自动"）与行为带规则摘要；
#   set   —— 切换路由模式（写入 .route_state，route.sh 优先采用，优先级高于自动分类）。
ROUTER_STATE_FILE="$(cd "$(dirname "$0")" && pwd)/../.route_state"
export ROUTER_STATE_FILE
exec python3 -c '
import json, sys, os

state_file = os.environ.get("ROUTER_STATE_FILE", "")

def read_state():
    try:
        if os.path.isfile(state_file):
            v = open(state_file).read().strip()
            if v in ("spec", "react", "mixed", "weak"):
                return v
    except Exception:
        pass
    return ""

def write_state(v):
    try:
        with open(state_file, "w") as f:
            f.write(v + "\n")
        return True
    except Exception:
        return False

d = {}
try:
    d = json.load(sys.stdin)
except Exception:
    pass

act = str(d.get("mode", "") or "query").lower()
val = str(d.get("value", "") or "").lower()
cur = read_state()

if act == "set":
    if val not in ("spec", "react", "mixed", "weak"):
        print(json.dumps({"ok": False, "error": "value 必须为 spec/react/mixed/weak 之一", "current": cur}, ensure_ascii=False))
    elif write_state(val):
        print(json.dumps({"ok": True, "mode": val, "current": val, "note": "已切换，route.sh 将优先采用此模式"}, ensure_ascii=False))
    else:
        print(json.dumps({"ok": False, "error": "状态文件写入失败", "current": cur}, ensure_ascii=False))
else:  # query（默认）
    bands = ["spec=计划者(先规划后执行)", "react=执行者(直接动手)", "mixed=混合(分阶段推进)", "weak=模型自分类"]
    print(json.dumps({"ok": True, "mode": cur if cur else "auto", "bands": bands,
                      "note": "set 可切换路由模式；auto=按任务关键词自动分类"}, ensure_ascii=False))
'
