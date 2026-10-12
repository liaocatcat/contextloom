#!/usr/bin/env bash
# e2e 边界：1) 任何地方都无配置 → 打印"未找到配置文件"并以内置默认启动
#          2) 无符号链接直接执行（exe 目录无配置 + CWD 有配置）→ 回退 CWD（向后兼容）
set -u
PROJ="$(cd "$(dirname "$0")/.." && pwd)"
cd "$PROJ"

E2E=$(mktemp -d)
BIN="$E2E/bin"
EMPTYCWD="$E2E/emptycwd"
CWDCFG="$E2E/cwdcfg"
mkdir -p "$BIN" "$EMPTYCWD" "$CWDCFG"
go build -o "$BIN/contextloom" ./cmd/contextloom || { echo "BUILD_FAIL"; exit 1; }

# 场景 1：全无配置
(
  cd "$EMPTYCWD"
  HOME="$E2E/nohome" "$BIN/contextloom" --serve > "$E2E/out1.log" 2> "$E2E/err1.log"
) &
P1=$!
sleep 3
kill "$P1" 2>/dev/null
wait "$P1" 2>/dev/null
echo "===场景1 stderr==="
head -3 "$E2E/err1.log"
grep -q "未找到配置文件，使用内置默认配置" "$E2E/err1.log" && echo "PASS: 无配置场景提示正确" || echo "FAIL: 未打印内置默认提示"

# 场景 2：无链接直接执行，exe 目录无配置、CWD 有配置 → 回退 CWD（原语义不变）
printf 'model:\n  backend: deepseek-v4\n  api_key: "sk-cwd-test"\n' > "$CWDCFG/config.yaml"
(
  cd "$CWDCFG"
  HOME="$E2E/nohome" "$BIN/contextloom" --serve > "$E2E/out2.log" 2> "$E2E/err2.log"
) &
P2=$!
sleep 3
kill "$P2" 2>/dev/null
wait "$P2" 2>/dev/null
echo "===场景2 stderr==="
head -3 "$E2E/err2.log"
grep -o "\[config\] 使用配置文件: .*" "$E2E/err2.log" | head -1
CFG_LINE=$(grep "\[config\] 使用配置文件" "$E2E/err2.log" | head -1)
if echo "$CFG_LINE" | grep -q "cwdcfg/config.yaml"; then
  echo "PASS: 直接执行回退 CWD 语义不变"
else
  echo "FAIL: 回退 CWD 语义被破坏 → $CFG_LINE"
fi

rm -rf "$E2E"
