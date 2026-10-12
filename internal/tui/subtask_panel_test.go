package tui

import (
	"strings"
	"testing"
)

// TestSubTaskPanel_FullSignalFlow 验证 TUI 子任务面板的完整信号链路：
// §START → 创建面板项 → §TOK → 更新 LastLine → §DONE → 标记完成 → renderTaskPanel 渲染。
// 若此测试通过，说明 TUI 层渲染逻辑正确；子任务不显示的根因只能是
// runtime 层信号未到达（即模型未调用 spawn_task，已由 runtime 测试独立验证链路）。
func TestSubTaskPanel_FullSignalFlow(t *testing.T) {
	m := initialModel(nil)

	// 1. §START 创建面板项
	m.handleSubTaskSignal("§START|sub-1|CODE_AGENT|实现登录函数")
	if len(m.subTasks) != 1 {
		t.Fatalf("§START 后应有 1 个面板项，got %d", len(m.subTasks))
	}
	st := m.subTasks[0]
	if st.ID != "sub-1" || st.Task != "实现登录函数" || st.Directive != "CODE_AGENT" || st.Status != "running" {
		t.Fatalf("面板项数据错误: %+v", st)
	}

	// 2. §TOK 更新 LastLine
	m.handleSubTaskSignal("§TOK|sub-1|正在读取文件")
	m.handleSubTaskSignal("§TOK|sub-1|auth.py\n开始写入")
	if m.subTasks[0].LastLine == "启动中…" || m.subTasks[0].LastLine == "" {
		t.Errorf("§TOK 后 LastLine 应更新，got %q", m.subTasks[0].LastLine)
	}
	// 含换行的 token 应取最后一行
	if !strings.Contains(m.subTasks[0].LastLine, "开始写入") {
		t.Errorf("§TOK 含换行应取最后一行，got %q", m.subTasks[0].LastLine)
	}

	// 3. §DONE 标记完成 + 评分
	m.handleSubTaskSignal("§DONE|sub-1|90")
	if m.subTasks[0].Status != "done" {
		t.Errorf("§DONE 后状态应为 done，got %q", m.subTasks[0].Status)
	}
	if m.subTasks[0].Score != 90 {
		t.Errorf("§DONE 后评分应为 90，got %v", m.subTasks[0].Score)
	}

	// 4. subTaskPanelHeight
	h := subTaskPanelHeight(m)
	if h != 2 { // 标题行 + 1 项
		t.Errorf("subTaskPanelHeight 应为 2（1项+标题），got %d", h)
	}

	// 5. renderTaskPanel 渲染含子任务信息
	panel := renderTaskPanel(m, 100)
	if panel == "" {
		t.Fatal("renderTaskPanel 应返回非空（有子任务）")
	}
	if !strings.Contains(panel, "sub-1") {
		t.Errorf("面板应含 sub-1，got %q", panel)
	}
	if !strings.Contains(panel, "实现登录") {
		t.Errorf("面板应含任务描述，got %q", panel)
	}
	if !strings.Contains(panel, "CODE_AGENT") {
		t.Errorf("面板应含指令类型，got %q", panel)
	}
	// 完成状态应显示评分
	if !strings.Contains(panel, "90") {
		t.Errorf("完成子任务面板应含评分 90，got %q", panel)
	}
	t.Logf("面板渲染结果:\n%s", panel)
}

// TestSubTaskPanel_MultipleParallel 多个并行子任务同时存在
func TestSubTaskPanel_MultipleParallel(t *testing.T) {
	m := initialModel(nil)
	// 两个子任务并行启动
	m.handleSubTaskSignal("§START|sub-1|CODE_AGENT|实现功能A")
	m.handleSubTaskSignal("§START|sub-2|TEST|编写测试B")
	if len(m.subTasks) != 2 {
		t.Fatalf("应有 2 个并行面板项，got %d", len(m.subTasks))
	}
	if m.subTasks[0].ID != "sub-1" || m.subTasks[1].ID != "sub-2" {
		t.Error("两个子任务 ID 顺序错误")
	}
	// 面板高度 = 2 项 + 标题 = 3
	if subTaskPanelHeight(m) != 3 {
		t.Errorf("2项面板高度应为 3，got %d", subTaskPanelHeight(m))
	}
	panel := renderTaskPanel(m, 100)
	if !strings.Contains(panel, "sub-1") || !strings.Contains(panel, "sub-2") {
		t.Errorf("面板应含两个子任务，got %q", panel)
	}
}

// TestSubTaskPanel_FailSignal §FAIL 信号标记失败
func TestSubTaskPanel_FailSignal(t *testing.T) {
	m := initialModel(nil)
	m.handleSubTaskSignal("§START|sub-1|CODE_AGENT|任务X")
	m.handleSubTaskSignal("§FAIL|sub-1|连接超时")
	if m.subTasks[0].Status != "failed" {
		t.Errorf("§FAIL 后状态应为 failed，got %q", m.subTasks[0].Status)
	}
	if !strings.Contains(m.subTasks[0].LastLine, "连接超时") {
		t.Errorf("§FAIL 后 LastLine 应含错误，got %q", m.subTasks[0].LastLine)
	}
}

// TestSubTaskPanel_Empty 无子任务时面板不渲染
func TestSubTaskPanel_Empty(t *testing.T) {
	m := initialModel(nil)
	if subTaskPanelHeight(m) != 0 {
		t.Error("无子任务时面板高度应为 0")
	}
	if renderTaskPanel(m, 80) != "" {
		t.Error("无子任务时面板应为空字符串")
	}
}

// TestSubTaskPanel_EmployeeField 新格式 §START 携带员工段 → 面板项 Employee 字段 + 渲染显示员工名。
func TestSubTaskPanel_EmployeeField(t *testing.T) {
	m := initialModel(nil)
	m.handleSubTaskSignal("§START|sub-1|CODE_AGENT|coder|实现登录函数")
	if len(m.subTasks) != 1 {
		t.Fatalf("§START 后应有 1 个面板项，got %d", len(m.subTasks))
	}
	st := m.subTasks[0]
	if st.Employee != "coder" {
		t.Fatalf("Employee 字段应为 coder，got %q", st.Employee)
	}
	if st.Directive != "CODE_AGENT" || st.Task != "实现登录函数" {
		t.Fatalf("Directive/Task 解析错误: %+v", st)
	}
	panel := renderTaskPanel(m, 100)
	if !strings.Contains(panel, "coder") {
		t.Errorf("面板应显示员工名 coder，got %q", panel)
	}
	if strings.Contains(panel, "sub-1") {
		t.Errorf("员工命中时面板不应显示 subID，got %q", panel)
	}
}

// TestSubTaskPanel_EmployeeReset 同一 subID 新格式重复 START → 重置员工字段（不残留旧值）。
func TestSubTaskPanel_EmployeeReset(t *testing.T) {
	m := initialModel(nil)
	m.handleSubTaskSignal("§START|sub-1|CODE_AGENT|实现登录函数")
	m.handleSubTaskSignal("§START|sub-1|TEST|tester|编写测试")
	if m.subTasks[0].Employee != "tester" {
		t.Fatalf("重复 START 后 Employee 应更新为 tester，got %q", m.subTasks[0].Employee)
	}
	if m.subTasks[0].Directive != "TEST" || m.subTasks[0].Task != "编写测试" {
		t.Fatalf("重复 START 后 Directive/Task 应更新: %+v", m.subTasks[0])
	}
}

// TestSubTaskPanel_LegacyFormat 旧格式 §START（无员工段）→ Employee 为空，渲染回退 subID。
func TestSubTaskPanel_LegacyFormat(t *testing.T) {
	m := initialModel(nil)
	m.handleSubTaskSignal("§START|sub-1|CODE_AGENT|实现登录函数")
	if m.subTasks[0].Employee != "" {
		t.Fatalf("旧格式 Employee 应为空，got %q", m.subTasks[0].Employee)
	}
	panel := renderTaskPanel(m, 100)
	if !strings.Contains(panel, "sub-1") {
		t.Errorf("旧格式（无员工）面板应回退显示 subID，got %q", panel)
	}
}
