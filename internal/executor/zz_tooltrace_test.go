package executor

import "testing"

// hasToolCallTrace：工具调用痕迹识别（agent 层"不中止对话"兜底的前提）
func TestHasToolCallTrace(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"XML 截断痕迹", "我先看一下目录。\n<tool_calls>\n<invoke name=\"list_files\">\n<parameter name=\"dir\">/tmp", true},
		{"DSML 痕迹", "调用：<|invoke name=\"read_file\"|>", true},
		{"tool_call 行", "使用 [tool_call_abc123] exec_command: ls 执行", true},
		{"围栏痕迹", "```tool_call\nlist_files: /tmp", true},
		{"普通正文", "我先查看项目根目录结构，了解整体布局。", false},
		{"提到工具名但不是调用", "我可以用 read_file 读取文件，也可以调用 exec_command。", false},
		{"完全无关", "你好，今天天气不错。", false},
	}
	for _, c := range cases {
		if got := hasToolCallTrace(c.input); got != c.want {
			t.Errorf("%s: hasToolCallTrace(%q) = %v, want %v", c.name, c.input, got, c.want)
		}
	}
}
