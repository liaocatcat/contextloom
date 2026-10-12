// Command pecheck 校验 Windows PE 可执行文件的 Subsystem 值。
//
// 用途：contextloom-gui 必须是 GUI 子系统(2)；若为 CONSOLE(3)，用户双击会弹出
// 黑色 cmd 窗口（历史回归 bug 的根因——构建命令漏掉 -H windowsgui）。
// 该工具挂在 Makefile 的 build-gui / build-gui-win / verify-gui 上，
// 任何未来构建若漏掉标志会立即报错退出，杜绝静默产出错误产物。
//
// 用法：go run ./scripts/pecheck <pe-file> [期望subsystem，默认2]
package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pecheck <pe-file> [want-subsystem=2]")
		os.Exit(2)
	}
	path := os.Args[1]
	want := 2
	if len(os.Args) >= 3 {
		if _, err := fmt.Sscanf(os.Args[2], "%d", &want); err != nil {
			fmt.Fprintf(os.Stderr, "pecheck: 非法期望值 %q: %v\n", os.Args[2], err)
			os.Exit(2)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pecheck: %v\n", err)
		os.Exit(1)
	}
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		fmt.Fprintf(os.Stderr, "pecheck: %s 不是有效的 PE 文件\n", path)
		os.Exit(1)
	}
	peOff := int(binary.LittleEndian.Uint32(b[0x3C:]))
	if peOff+4+20+68+2 > len(b) || string(b[peOff:peOff+4]) != "PE\x00\x00" {
		fmt.Fprintf(os.Stderr, "pecheck: %s PE 头损坏\n", path)
		os.Exit(1)
	}
	// OptionalHeader.Subsystem 位于可选头偏移 68（PE32 与 PE32+ 相同）
	sub := binary.LittleEndian.Uint16(b[peOff+4+20+68:])
	name := "?"
	switch sub {
	case 2:
		name = "GUI"
	case 3:
		name = "CONSOLE"
	}
	if int(sub) != want {
		fmt.Fprintf(os.Stderr,
			"pecheck: %s subsystem=%d(%s) != 期望 %d —— GUI 构建必须带 -H windowsgui（否则双击弹黑窗）\n",
			path, sub, name, want)
		os.Exit(1)
	}
	fmt.Printf("pecheck: %s subsystem=%d(%s) OK\n", path, sub, name)
}
