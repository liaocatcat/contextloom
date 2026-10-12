package tui

import (
	"testing"
)

func TestParseSplitArgs(t *testing.T) {
	tests := []struct {
		name string
		args string
		want []string
	}{
		{"英文逗号", "实现登录, 编写测试", []string{"实现登录", "编写测试"}},
		{"中文逗号", "实现登录，编写测试", []string{"实现登录", "编写测试"}},
		{"分号", "任务A; 任务B; 任务C", []string{"任务A", "任务B", "任务C"}},
		{"混合分隔符", "任务A, 任务B；任务C，任务D", []string{"任务A", "任务B", "任务C", "任务D"}},
		{"去除空白", "  任务A  ,  任务B  ", []string{"任务A", "任务B"}},
		{"空字符串", "", []string{}},
		{"单个任务", "仅一个任务", []string{"仅一个任务"}},
		{"尾部逗号", "任务A, 任务B,", []string{"任务A", "任务B"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSplitArgs(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("len: got %d want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d]: got %q want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestParseSplitArgs_RealisticExamples 验证真实使用场景的解析正确性
func TestParseSplitArgs_RealisticExamples(t *testing.T) {
	// 场景：并行实现 + 测试
	got := parseSplitArgs("实现用户注册功能写入 auth.py, 为注册接口编写单元测试 test_auth.py")
	if len(got) != 2 {
		t.Fatalf("期望 2 个任务，got %d: %v", len(got), got)
	}
	if got[0] != "实现用户注册功能写入 auth.py" {
		t.Errorf("任务0: %q", got[0])
	}

	// 场景：三任务并行
	got = parseSplitArgs("重构 auth 模块，更新数据库迁移脚本，添加 API 文档")
	if len(got) != 3 {
		t.Errorf("期望 3 个任务，got %d: %v", len(got), got)
	}
}

// TestSendSplit_ValidationErrors 验证 sendSplit 的参数校验错误路径（不依赖运行时）
func TestSendSplit_ValidationErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"无参数", "/split"},
		{"单个任务", "/split 仅一个任务"},
		{"空分隔", "/split ,,"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := initialModel(nil)
			before := len(m.messages)
			_, _ = m.sendSplit(tt.input)
			// 校验失败时应添加一条系统提示消息
			if len(m.messages) != before+1 {
				t.Errorf("期望添加 1 条提示消息，before=%d after=%d", before, len(m.messages))
			}
			// 不应进入 streaming 模式
			if m.streaming {
				t.Error("校验失败不应进入 streaming")
			}
		})
	}
}

// TestSendSplit_NoRuntime 验证无运行时连接时报错
func TestSendSplit_NoRuntime(t *testing.T) {
	m := initialModel(nil)
	_, _ = m.sendSplit("/split 任务A, 任务B")
	// 应有错误消息
	if len(m.messages) == 0 {
		t.Fatal("期望添加错误消息")
	}
	last := m.messages[len(m.messages)-1]
	if last.Type != MsgError {
		t.Errorf("期望错误类型消息，got %v", last.Type)
	}
}

// TestSendSplit_TriggersPanel 验证 sendSplit 设置 subTasks=nil 后进入 streaming（面板数据源就绪）
// 注意：完整面板渲染验证已在 TestSubTaskPanel_* 中通过 § 信号链路测试覆盖
func TestSendSplit_TriggersStreaming(t *testing.T) {
	// 此测试需要 runtime，留作集成测试；此处仅验证参数解析和行为状态
	// runtime 层 § 信号链路已由 TestSpawnTask_EmitsSubTaskSignals 覆盖
	// tui 层面板渲染已由 TestSubTaskPanel_* 覆盖
	t.Skip("集成测试：需真实 runtime，链路由 runtime/tui 层测试覆盖")
}
