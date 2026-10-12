package tui

import (
	"strings"
	"testing"
)

// TestRenderMessage_SystemMultiLine：多行系统消息（/emp 员工清单等）渲染时
// 必须保留全部行，仅对超宽行逐行截断。
// 回归背景：旧实现 truncMsg(整串, w) 按 rune 截断，超宽首行之后的所有行被丢弃，
// 表现为"/emp 显示员工总数 6 名却只列出 2 条"（用户反馈）。
func TestRenderMessage_SystemMultiLine(t *testing.T) {
	content := "可用员工 6 名:\n" +
		"  coder · 编码 · 专精: 实现/调试 · 工作区: coder\n" +
		"  product · 产品\n" +
		"  reviewer · 评审\n" +
		"  tester · 测试\n" +
		"  translator · 翻译\n" +
		"  writer · 写作"
	msg := sysMsg(content)
	// 宽度取较窄值，确保部分行超宽触发截断
	out := renderMessage(msg, 60, false)

	for _, want := range []string{"coder", "product", "reviewer", "tester", "translator", "writer"} {
		if !strings.Contains(out, want) {
			t.Errorf("多行系统消息应保留全部员工行，缺失 %q\n渲染结果:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "可用员工 6 名") {
		t.Errorf("应保留首行标题，渲染结果:\n%s", out)
	}
	// 行数应完整保留（7 行内容）
	if got := strings.Count(out, "\n"); got < 6 {
		t.Errorf("应保留 7 行内容（≥6 个换行），got %d 个换行\n%s", got, out)
	}
}

// TestTruncMsgLines：逐行截断语义——单行退化为 truncMsg，多行逐行处理且行数不变。
func TestTruncMsgLines(t *testing.T) {
	if got := truncMsgLines("hello", 10); got != "hello" {
		t.Errorf("单行短文本不变: %q", got)
	}
	if got := truncMsgLines("hello world", 5); got != "he..." {
		t.Errorf("单行超长按 rune 截断: %q", got)
	}
	multi := "aaaaaaaaaa\nb\ncccccccccc"
	got := truncMsgLines(multi, 4)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("逐行截断应保留 3 行, got %d: %q", len(lines), got)
	}
	if lines[0] != "a..." || lines[1] != "b" || lines[2] != "c..." {
		t.Errorf("逐行截断结果不符: %q", lines)
	}
}
