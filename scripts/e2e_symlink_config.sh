#!/usr/bin/env bash
# e2e: 符号链接全局命令场景 —— 真实二进制目录放好配置 + CWD 放坏配置
# 期望（修复后）：读取真实二进制目录配置；stderr 打印配置来源；无 CWD 解析错误
set -u
PROJ="$(cd "$(dirname "$0")/.." && pwd)"
cd "$PROJ"

E2E=$(mktemp -d)
REALBIN="$E2E/realbin"
LINKBIN="$E2E/linkbin"
BADCWD="$E2E/badcwd"
mkdir -p "$REALBIN" "$LINKBIN" "$BADCWD"

go build -o "$REALBIN/contextloom" ./cmd/contextloom || { echo "BUILD_FAIL"; exit 1; }
echo "BUILD_OK"

# 好配置（真实二进制本体目录）
printf 'model:\n  backend: deepseek-v4\n  endpoint: https://api.deepseek.com\n  api_key: "sk-e2e-test"\n' > "$REALBIN/config.yaml"
# 坏配置（CWD，不可解析 YAML）
printf 'model:\n  api_key: [unclosed-bad-yaml\n' > "$BADCWD/config.yaml"
ln -s "$REALBIN/contextloom" "$LINKBIN/contextloom"

(
  cd "$BADCWD"
  PATH="$LINKBIN:$PATH" contextloom --serve > "$E2E/out.log" 2> "$E2E/err.log"
) &
PID=$!
sleep 3
kill "$PID" 2>/dev/null
wait "$PID" 2>/dev/null

echo "===STDERR==="
head -8 "$E2E/err.log"
echo "===STDOUT==="
head -3 "$E2E/out.log"
echo "===断言==="
grep -q "\[config\] 使用配置文件" "$E2E/err.log" && echo "PASS: 配置来源已打印" || echo "FAIL: 未打印配置来源"
if grep -q "无法加载配置" "$E2E/err.log"; then echo "FAIL: 仍报 CWD 配置解析错误"; else echo "PASS: 未读 CWD 坏配置（无解析错误）"; fi
grep -o "\[config\] 使用配置文件: .*" "$E2E/err.log" | head -1

# 配置路径必须落在 REALBIN（允许 /private 前缀的符号链接归一化）
CFG_LINE=$(grep "\[config\] 使用配置文件" "$E2E/err.log" | head -1)
if echo "$CFG_LINE" | grep -q "realbin/config.yaml"; then
  echo "PASS: 配置路径命中真实二进制本体目录"
else
  echo "FAIL: 配置路径不是真实二进制本体目录 → $CFG_LINE"
fi

# 清理临时目录
rm -rf "$E2E"
