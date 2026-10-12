#!/bin/sh
# route.sh —— route 钩子脚本：思维模式路由决策（对标 dsh-routing-suite preset）
#
# 契约（见 internal/plugin/protocol.go RouteHook / runtime.go runPluginRouteHooks）：
#   stdin  = JSON {mode, input, round}
#   stdout = 路由引导文本（纯文本，原样注入 system 区）；空/失败 = 不注入
#
# 三行为带路由（dsh preset 核心机制）：
#   spec  —— 计划者：任务含规划/设计/重构/架构/方案类特征 → 先规划后执行
#   react —— 执行者：任务含修复/实现/编写/删除/运行类直接动作 → 直接动手
#   mixed —— 陷阱回避：规划+执行混合特征 → 分阶段推进，避免任一极端
#   weak  —— 模型自分类：无法判断 → 引导模型自行选择节奏
#
# 优先级：用户覆盖（dev_router_mode set 写入的状态文件）> 关键词自动分类 > mode 兜底。
ROUTER_STATE_FILE="$(cd "$(dirname "$0")" && pwd)/../.route_state"
export ROUTER_STATE_FILE
exec python3 -c '
import json, sys, os

d = {}
try:
    d = json.load(sys.stdin)
except Exception:
    pass

mode = str(d.get("mode", "") or "").lower()
inp  = str(d.get("input", "") or "")

# ── 0. 用户覆盖（AI 自优化：dev_router_mode set 写入的状态文件，优先级最高）──
state_file = os.environ.get("ROUTER_STATE_FILE", "")
override = ""
try:
    if os.path.isfile(state_file):
        ov = open(state_file).read().strip()
        if ov in ("spec", "react", "mixed", "weak"):
            override = ov
except Exception:
    pass

# ── 1. 关键词自动分类（启发式，与 dsh preset 行为带分类对齐）──
def classify(text):
    t = text.lower()
    mixed_kw = ["先设计", "先规划", "设计并实现", "规划并", "从零实现", "端到端", "全流程"]
    spec_kw  = ["规划", "设计", "重构", "架构", "方案", "计划", "拆解", "设计文档", "流程图", "架构设计", "设计稿", "调研"]
    react_kw = ["修复", "实现", "编写", "删除", "删掉", "运行", "测试", "改", "添加", "新增", "构建", "编译", "部署", "执行", "写出", "创建"]
    for kw in mixed_kw:
        if kw in t:
            return "mixed"
    for kw in spec_kw:
        if kw in t:
            return "spec"
    for kw in react_kw:
        if kw in t:
            return "react"
    return "weak"

if override:
    band = override
else:
    band = classify(inp)
    if band == "weak":
        # mode 兜底：goal→spec（计划者）/ task→react（执行者）/ chat→mixed
        band = {"goal": "spec", "task": "react"}.get(mode, "mixed")

# ── 2. 按行为带输出 persona 提示词段（注入 system 区，随输入/轮次动态决策）──
if band == "spec":
    print("【路由决策 · spec 计划者】本任务宜先规划后执行：")
    print("1. 先拆解目标与验收标准，输出步骤清单；")
    print("2. 按清单逐步执行，每步验证产出；")
    print("3. 全部完成后统一自查。避免跳过规划直接动手。")
elif band == "react":
    print("【路由决策 · react 执行者】本任务宜直接执行：")
    print("1. 识别最小可行动作立即实施；")
    print("2. 边做边验证，及时修正偏差；")
    print("3. 完成后自查结果。避免过度规划拖延产出。")
elif band == "mixed":
    print("【路由决策 · mixed 混合】本任务含规划与执行双重成分：")
    print("1. 先做轻量规划（目标+步骤），不过度设计；")
    print("2. 立即进入实现，按步骤推进；")
    print("3. 分阶段验证，阶段间保持收敛。避免陷入规划或盲动任一极端。")
else:  # weak
    print("【路由决策 · weak 模型自分类】任务类型不明确，请自行判断节奏：")
    print("- 需先规划（spec）/ 直接执行（react）/ 混合推进（mixed）；")
    print("- 选定后按对应节奏推进，并明确可验证的验收标准。")
'
