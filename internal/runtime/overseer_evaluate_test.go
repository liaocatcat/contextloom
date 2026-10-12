package runtime

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/overseer"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestOverseerEvaluate_HighQualityPasses 高质量响应通过评估
func TestOverseerEvaluate_HighQualityPasses(t *testing.T) {
	rt := newRuntimeWithOverseer(t, 90)
	response := "已实现登录功能，所有测试通过，go test 显示 100% 覆盖率。\n```go\n// Login 登录\nfunc Login(u, p string) (string, error) {\n\treturn \"token\", nil\n}\n```"
	passed, score, report := rt.OverseerEvaluate(response)
	if !passed {
		t.Errorf("高质量响应应通过，评分 %.1f", score)
	}
	if score < 90 {
		t.Errorf("高质量响应应 >=90，got %.1f", score)
	}
	if report.Passed != passed {
		t.Errorf("report.Passed 与返回值不一致: %v vs %v", report.Passed, passed)
	}
	if report.Score != score {
		t.Errorf("report.Score 与返回值不一致: %.1f vs %.1f", report.Score, score)
	}
}

// TestOverseerEvaluate_ConstraintFailure 代码质量低触发约束失败
func TestOverseerEvaluate_ConstraintFailure(t *testing.T) {
	// 约束 min_code_quality=70，构造无注释长函数（惩罚 0.30 → 70 分 → <约束）
	rt := newRuntimeWithOverseer(t, 70, "min_code_quality", 80.0)
	longBody := strings.Repeat("\tx := 1\n", 60)
	response := "```go\nfunc Process(items []string) {\n" + longBody + "}\n```"
	passed, score, report := rt.OverseerEvaluate(response)
	if passed {
		t.Errorf("低质量代码应未通过约束，评分 %.1f 质量 %.1f", score, report.CodeQuality)
	}
	if report.CodeQuality >= 80 {
		t.Errorf("代码质量应 <80，got %.1f", report.CodeQuality)
	}
}

// TestOverseerEvaluate_EmptyResponse 空响应低完整度未通过
func TestOverseerEvaluate_EmptyResponse(t *testing.T) {
	rt := newRuntimeWithOverseer(t, 90)
	passed, score, _ := rt.OverseerEvaluate("   ")
	if passed {
		t.Errorf("空响应应未通过，评分 %.1f", score)
	}
	if score >= 90 {
		t.Errorf("空响应应 <90，got %.1f", score)
	}
}

// TestOverseerEvaluate_NoOverseer 无监工时直通
func TestOverseerEvaluate_NoOverseer(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "contextloom-test-*")
	defer os.RemoveAll(tmpDir)
	rt, _ := New(testConfig(tmpDir))
	rt.Config.CLIDefaults.Mode = "chat" // chat 模式无监工
	passed, score, report := rt.OverseerEvaluate("任意内容")
	if !passed || score != 100 {
		t.Errorf("无监工应直通 passed=true score=100，got passed=%v score=%.1f", passed, score)
	}
	if report.NodeName != "" {
		t.Errorf("无监工应返回空报告，got %+v", report)
	}
}

// TestOverseerEvaluate_DimensionRange 验证四维分数在 0-100 范围且量纲一致
func TestOverseerEvaluate_DimensionRange(t *testing.T) {
	rt := newRuntimeWithOverseer(t, 90)
	_, _, report := rt.OverseerEvaluate("普通响应内容，长度超过二十个字符用于测试。")
	for _, v := range []float64{report.Completeness, report.TestCoverage, report.CodeQuality, report.NoSideEffect} {
		if v < 0 || v > 100 {
			t.Errorf("四维分数应 0-100，got %.1f", v)
		}
	}
}

// TestOverseerEvaluate_LastAgentIDAssociated 验证评估关联最近请求的 Agent ID
func TestOverseerEvaluate_LastAgentIDAssociated(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "contextloom-test-*")
	defer os.RemoveAll(tmpDir)
	rt, _ := New(testConfig(tmpDir))
	rt.Backend = &multiTokenBackend{tokens: []string{"已实现功能，测试通过。"}}
	rt.Config.CLIDefaults.Mode = "task"
	ctx := context.Background()
	ch, _, err := rt.ProcessInput(ctx, "单任务")
	if err != nil {
		t.Fatalf("ProcessInput error: %v", err)
	}
	for range ch {
	}

	rt.overseerMu.RLock()
	agentID := rt.lastAgentID
	rt.overseerMu.RUnlock()
	if agentID == "" {
		t.Fatal("lastAgentID 应记录非空")
	}

	// 评估应在 checkpoint 记录中关联该 agent
	_, _, _ = rt.OverseerEvaluate("已实现功能，测试通过。")
	cps := rt.currentOverseer.Checkpoints()
	found := false
	for _, cp := range cps {
		if cp.ChildAgentID == agentID {
			found = true
		}
	}
	if !found {
		t.Errorf("评估未关联 lastAgentID=%q，checkpoints=%+v", agentID, cps)
	}
}

// TestOverseerEvaluate_NonCodeTaskNotPenalized 非代码任务（删文件/配置）不应因测试覆盖被误判
func TestOverseerEvaluate_NonCodeTaskNotPenalized(t *testing.T) {
	rt := newRuntimeWithOverseer(t, 90)
	// 删除文件任务的响应——无代码块，无测试关键词，但任务正常完成
	response := "已删除 ~/.contextloom/memory/ 下的全部 35 个会话文件和 index.bolt 索引数据库，记忆系统现在为空。"
	passed, score, report := rt.OverseerEvaluate(response)
	if !passed {
		t.Errorf("非代码任务正常完成应通过，评分 %.1f（测试覆盖 %.1f 不应拉低总分）", score, report.TestCoverage)
	}
	if report.TestCoverage < 90 {
		t.Errorf("非代码任务测试覆盖不应扣分，got %.1f", report.TestCoverage)
	}
	if score < 90 {
		t.Errorf("非代码任务总分应 >=90，got %.1f", score)
	}
}

// TestOverseerEvaluate_CodeTaskWithoutTests 代码任务缺测试应扣分
func TestOverseerEvaluate_CodeTaskWithoutTests(t *testing.T) {
	rt := newRuntimeWithOverseer(t, 90)
	// 代码任务但未提测试——应因测试覆盖低而未通过
	response := "已实现登录功能。\n```go\nfunc Login(u, p string) (string, error) {\n\treturn \"token\", nil\n}\n```"
	_, score, report := rt.OverseerEvaluate(response)
	if report.TestCoverage > 60 {
		t.Errorf("代码任务缺测试应严重扣分，测试覆盖 got %.1f", report.TestCoverage)
	}
	_ = score
}

// newRuntimeWithOverseer 构造带监工的 runtime 用于测试
func newRuntimeWithOverseer(t *testing.T, passThreshold float64, constraints ...any) *Runtime {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "contextloom-overseer-eval-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	cfg := testConfig(tmpDir)
	cfg.Overseer.PassThreshold = passThreshold
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	rt.Backend = &multiTokenBackend{tokens: []string{"resp"}}

	// 强制 task 模式 + 直接创建带约束的监工
	rt.Config.CLIDefaults.Mode = "task"
	conMap := map[string]any{}
	for i := 0; i+1 < len(constraints); i += 2 {
		if k, ok := constraints[i].(string); ok {
			conMap[k] = constraints[i+1]
		}
	}
	rt.overseerMu.Lock()
	rt.currentOverseer = overseer.New("test", conMap, types.Score(passThreshold), nil)
	rt.lastAgentID = "test-agent"
	rt.overseerMu.Unlock()
	return rt
}
