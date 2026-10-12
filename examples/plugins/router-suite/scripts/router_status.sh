#!/bin/sh
# router_status.sh —— dev_router_status 工具实现脚本
#
# 契约：stdin 收到工具参数 JSON（本工具忽略参数），
#       stdout 输出 JSON 结果，原样作为工具结果返回给模型。
# 注意：stdout 只输出 JSON，不要混入日志（工具结果会被完整解析）。
cat >/dev/null
printf '%s\n' '{"status":"running","plugin":"router-suite"}'
