package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInputHistoryDir_NoRuntimeNoUserDir 回归：无运行时上下文的 model
// （测试/预览场景）inputHistoryDir 必须返回空——历史缺陷：退回用户级
// ~/.contextloom，导致 go test 把测试输入写入真实用户数据目录。
func TestInputHistoryDir_NoRuntimeNoUserDir(t *testing.T) {
	m := &model{} // 无 rt
	if got := m.inputHistoryDir(); got != "" {
		t.Fatalf("无 rt 时应返回空串（不读写用户级目录），got %q", got)
	}
}

// TestSend_NoRuntime_DoesNotWriteUserHistory 回归：无 rt 的 model 触发
// send（保存输入历史）不得在 ~/.contextloom 创建 input_history.txt。
func TestSend_NoRuntime_DoesNotWriteUserHistory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无 HOME: %v", err)
	}
	path := filepath.Join(home, ".contextloom", "input_history.txt")
	_ = os.Remove(path) // 清理历史测试污染（若存在）

	m := &model{input: "测试输入不应写入用户级"}
	m.send()

	if _, err := os.Stat(path); err == nil {
		t.Fatalf("无 rt 的 send 不应创建用户级 input_history.txt: %s", path)
	}
	_ = os.Remove(path) // 确保清理
}
