package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/selfcheck"
	"github.com/contextloom/contextloom-agent/internal/webhook"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// =============================================================================
// M5 质量账本持久化接线测试（recordGateVerdict → quality_ledger.yaml）
// =============================================================================

// failReport 测试辅助：构造含一条 high 静检缺陷（Kind=residue）的门禁报告。
// 账本签名 = Kind + ": " + Hint（与 recordGateVerdict 聚合规则一致）。
func failReport(hint string) selfcheck.QualityReport {
	return selfcheck.QualityReport{
		Static: selfcheck.StaticReport{
			Pass: false,
			Issues: []selfcheck.Issue{
				{File: "a.go", Line: 1, Kind: "residue", Hint: hint, Severity: "high"},
			},
		},
		PassScore: 90,
	}
}

// TestRecordGateVerdict_ZeroConfig 无 .contextloom/gates 目录 → 零操作
// （不建目录、不写文件，行为与旧版一致）。
func TestRecordGateVerdict_ZeroConfig(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.recordGateVerdict(failReport("TODO残留"), selfcheck.DecisionFail)
	ledgerPath := filepath.Join(tmpDir, ".contextloom", "gates", "quality_ledger.yaml")
	if _, err := os.Stat(ledgerPath); !os.IsNotExist(err) {
		t.Fatalf("零配置下不应产生账本文件: %v", err)
	}
}

// TestRecordGateVerdict_PassNoWrite Pass 裁决 → 零磁盘写入（即使 gates 目录存在）。
func TestRecordGateVerdict_PassNoWrite(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".contextloom", "gates"), 0755); err != nil {
		t.Fatal(err)
	}
	rt.recordGateVerdict(selfcheck.QualityReport{}, selfcheck.DecisionPass)
	ledgerPath := filepath.Join(tmpDir, ".contextloom", "gates", "quality_ledger.yaml")
	if _, err := os.Stat(ledgerPath); !os.IsNotExist(err) {
		t.Fatalf("Pass 不应落盘: %v", err)
	}
}

// TestRecordGateVerdict_FailPersists Fail 裁决 → 缺陷签名落盘 quality_ledger.yaml。
func TestRecordGateVerdict_FailPersists(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".contextloom", "gates"), 0755); err != nil {
		t.Fatal(err)
	}
	rt.recordGateVerdict(failReport("TODO残留"), selfcheck.DecisionFail)
	ledgerPath := filepath.Join(tmpDir, ".contextloom", "gates", "quality_ledger.yaml")
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("账本文件应已落盘: %v", err)
	}
	var l selfcheck.Ledger
	if err := l.UnmarshalYAML(data); err != nil {
		t.Fatalf("账本 YAML 反序列化失败: %v", err)
	}
	if l.Count("residue: TODO残留") != 1 {
		t.Fatalf("签名应记录 1 次，账本内容: %s", data)
	}
}

// TestRecordGateVerdict_Aggregates 同签名跨轮聚合 +1；TEE 失败签名入账。
func TestRecordGateVerdict_Aggregates(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".contextloom", "gates"), 0755); err != nil {
		t.Fatal(err)
	}
	// 第一轮：静检缺陷 + TEE 失败
	rep := failReport("TODO残留")
	rep.TEE = &selfcheck.TestTEEResult{ArtifactType: selfcheck.ArtifactGoCode, Compiled: false, TestPassed: false, Output: "boom"}
	rt.recordGateVerdict(rep, selfcheck.DecisionFail)
	// 第二轮：同签名再出现 → Count +1
	rt.recordGateVerdict(failReport("TODO残留"), selfcheck.DecisionFail)

	ls := rt.gateLedger()
	if ls.ledger.Count("residue: TODO残留") != 2 {
		t.Fatalf("同签名应聚合为 2，got %d", ls.ledger.Count("residue: TODO残留"))
	}
	if ls.ledger.Count("TEE失败: 测试TEE 未通过（go_code，compiled=false passed=false）：boom") != 1 {
		t.Fatalf("TEE 失败签名应入账 1 次: %v", ls.ledger.TopSuspects(10))
	}
}

// TestRecordGateVerdict_ReloadsFromDisk 重启（新 runtime 实例）后账本从磁盘恢复累计。
func TestRecordGateVerdict_ReloadsFromDisk(t *testing.T) {
	dir, err := os.MkdirTemp("", "contextloom-ledger-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".contextloom", "gates"), 0755); err != nil {
		t.Fatal(err)
	}
	// 进程一：记录一次缺陷
	rt1 := newTestRuntimeAt(t, dir)
	rt1.recordGateVerdict(failReport("TODO残留"), selfcheck.DecisionFail)
	// 进程二：同一项目根 → 新实例恢复账本并继续累计
	rt2 := newTestRuntimeAt(t, dir)
	rt2.recordGateVerdict(failReport("TODO残留"), selfcheck.DecisionFail)
	ls := rt2.gateLedger()
	if ls.ledger.Count("residue: TODO残留") != 2 {
		t.Fatalf("跨进程累计应为 2，got %d", ls.ledger.Count("residue: TODO残留"))
	}
}

// TestRecordGateVerdict_LowSeverityIgnored low 级缺陷不入账（与门禁 DefectsFromReport 语义一致）。
func TestRecordGateVerdict_LowSeverityIgnored(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".contextloom", "gates"), 0755); err != nil {
		t.Fatal(err)
	}
	rep := selfcheck.QualityReport{
		Static: selfcheck.StaticReport{
			Pass: true,
			Issues: []selfcheck.Issue{
				{File: "a.go", Line: 1, Kind: "residue", Hint: "TODO 提示", Severity: "low"},
			},
		},
		PassScore: 90,
	}
	rt.recordGateVerdict(rep, selfcheck.DecisionFail)
	ls := rt.gateLedger()
	if len(ls.ledger.TopSuspects(10)) != 0 {
		t.Fatalf("low 级缺陷不应入账: %v", ls.ledger.TopSuspects(10))
	}
}

// TestGateLedger_RecoversAfterDirCreated 零配置先访问（ready=false），随后 gates
// 目录被创建（GUI 门禁面板 gate.save 会创建目录）→ 同进程内下一轮恢复账本能力。
func TestGateLedger_RecoversAfterDirCreated(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	// 先零配置访问：ready 应为 false
	rt.recordGateVerdict(failReport("TODO残留"), selfcheck.DecisionFail)
	if ls := rt.gateLedger(); ls != nil && ls.ready {
		t.Fatal("目录不存在时账本不应就绪")
	}
	// GUI 场景：保存 gate.yaml（创建 gates 目录）
	if err := os.MkdirAll(filepath.Join(tmpDir, ".contextloom", "gates"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tmpDir, ".contextloom", "gates", "gate.yaml"), "level: standard\npass_score: 90\n")
	// 同进程下一轮 → 账本恢复并落盘
	rt.recordGateVerdict(failReport("TODO残留"), selfcheck.DecisionFail)
	ledgerPath := filepath.Join(tmpDir, ".contextloom", "gates", "quality_ledger.yaml")
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("目录创建后账本应恢复落盘: %v", err)
	}
}

// TestLedgerEndToEnd_GoalOnRoundEnd 端到端：GoalOnRoundEnd 门禁 Fail 时账本落盘。
// 构造真实 git 仓库 + 含 FIXME 的 .go 改动 → 静检 medium 缺陷 → 裁决 Fail。
func TestLedgerEndToEnd_GoalOnRoundEnd(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	// 激活门禁（gates 目录存在）
	if err := os.MkdirAll(filepath.Join(tmpDir, ".contextloom", "gates"), 0755); err != nil {
		t.Fatal(err)
	}
	// 建立 git 仓库并提交基线文件，再引入 FIXME 改动（进入 git diff --name-only）
	git := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		return cmd.Run()
	}
	_ = git("init", "-q")
	writeFile(t, filepath.Join(tmpDir, "a.go"), "package main\n\nfunc main() {}\n")
	_ = git("add", "a.go")
	_ = git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	writeFile(t, filepath.Join(tmpDir, "a.go"), "package main\n\n// FIXME: 待清理\nfunc main() {}\n")

	// 造一个 goal 会话，使 GoalOnRoundEnd 可执行到门禁段
	rt.Backend = &mockBackend{chatResponse: "ok"}
	if _, err := rt.RunChannelMessage(context.Background(), webhook.Message{Text: "写个功能", Mode: "goal"}); err != nil {
		t.Fatalf("创建 goal 会话失败: %v", err)
	}
	// 低分 + [GOAL_DONE] → 门禁静检命中 FIXME → Fail → 账本落盘
	_, _ = rt.GoalOnRoundEnd("完成 [GOAL_DONE]", true,
		types.Score(0), types.OverseerReport{})
	ledgerPath := filepath.Join(tmpDir, ".contextloom", "gates", "quality_ledger.yaml")
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("GoalOnRoundEnd 门禁 Fail 应触发账本落盘: %v", err)
	}
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("账本文件不应为空")
	}
}
