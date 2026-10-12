// Package goalmgr —— 证据链（Proof Chain）的确定性验证器。
//
// 对照 why-self-verification-is-unreliable.md §5.3：验收必须全量且外部，
// 模型自述不算证据。VerifyGoalProofs 只认三类物理通道：
//   - file：目标文件存在（os.Stat）
//   - diff：目标路径出现在 git diff --name-only（工作区实际改动）
//   - test：go test <target> 退出码 0（实测通过，带超时）
//
// 全部判定由确定性进程/文件系统完成，零模型依赖；IO 经 ProofIO 注入，
// 便于测试替换（不依赖真实 git 仓库）。
package goalmgr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ProofFailure 一条未通过的证据核验结果。
type ProofFailure struct {
	StepID string // 步骤 ID
	Kind   string // 证据类型
	Target string // 证据目标
	Reason string // 失败原因（可读）
}

// ProofIO 证据核验的 IO 依赖（测试注入点）。
type ProofIO struct {
	// ProjectRoot 项目根目录（file/diff/test 的基准）。
	ProjectRoot string
	// StatFile 检查文件是否存在；默认 os.Stat。
	StatFile func(path string) (os.FileInfo, error)
	// DiffNames 返回 git 工作区变更文件相对路径列表；默认 git diff --name-only。
	DiffNames func(root string) ([]string, error)
	// RunTest 执行测试并返回 (输出, 是否通过)；默认 go test <target>。
	RunTest func(root, target string, timeout time.Duration) (string, bool, error)
	// TestTimeout 测试执行超时（0 = 默认 120s）。
	TestTimeout time.Duration
}

// DefaultProofIO 默认 IO：真实文件系统 + git + go test。
func DefaultProofIO(projectRoot string) ProofIO {
	return ProofIO{
		ProjectRoot: projectRoot,
		StatFile:    os.Stat,
		DiffNames:   gitDiffNames,
		RunTest:     goTestRun,
	}
}

func gitDiffNames(root string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

func goTestRun(root, target string, timeout time.Duration) (string, bool, error) {
	ctx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", target)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), false, fmt.Errorf("go test 超时（%s）", target)
	}
	if err != nil {
		return string(out), false, nil
	}
	return string(out), true, nil
}

// VerifyGoalProofs 对步骤的 proofs 做确定性核验。
// 返回失败项列表；空列表 = 全部通过（或该步骤无 proofs，按兼容放行，
// 由调用方按 RequireEvidence 决定是否接受）。
//
// 语义（对照设计文档 §2.2）：
//   - 每条 proof 独立核验，失败项带可读 reason；
//   - 未知 kind → 失败（宁可拒绝，不放行）；
//   - test 类证据必须实际执行 go test 且退出码 0（模型口述"测试通过"不算）。
func VerifyGoalProofs(steps []types.GoalStep, io ProofIO) []ProofFailure {
	var out []ProofFailure
	if len(steps) == 0 {
		return nil
	}
	if io.StatFile == nil {
		io.StatFile = os.Stat
	}
	if io.DiffNames == nil {
		io.DiffNames = gitDiffNames
	}
	if io.RunTest == nil {
		io.RunTest = goTestRun
	}
	if io.TestTimeout == 0 {
		io.TestTimeout = 120 * time.Second
	}

	// diff 名称列表惰性获取（有 diff 类 proof 才执行 git）
	var diffNames []string
	diffLoaded := false

	for _, st := range steps {
		if st.Status != string(types.GoalStepDone) || len(st.Proofs) == 0 {
			continue
		}
		for _, p := range st.Proofs {
			switch p.Kind {
			case "file":
				fi, err := io.StatFile(filepath.Join(io.ProjectRoot, p.Target))
				if err != nil || fi.IsDir() {
					out = append(out, ProofFailure{
						StepID: st.ID, Kind: p.Kind, Target: p.Target,
						Reason: fmt.Sprintf("文件不存在或为目录: %s", p.Target),
					})
				}
			case "diff":
				if !diffLoaded {
					if names, err := io.DiffNames(io.ProjectRoot); err == nil {
						diffNames = names
					}
					diffLoaded = true
				}
				if !containsName(diffNames, p.Target) {
					out = append(out, ProofFailure{
						StepID: st.ID, Kind: p.Kind, Target: p.Target,
						Reason: fmt.Sprintf("git 工作区无该路径改动: %s（git diff --name-only 未命中）", p.Target),
					})
				}
			case "test":
				_, passed, err := io.RunTest(io.ProjectRoot, p.Target, io.TestTimeout)
				if err != nil {
					out = append(out, ProofFailure{
						StepID: st.ID, Kind: p.Kind, Target: p.Target,
						Reason: err.Error(),
					})
				} else if !passed {
					out = append(out, ProofFailure{
						StepID: st.ID, Kind: p.Kind, Target: p.Target,
						Reason: fmt.Sprintf("go test %s 未通过", p.Target),
					})
				}
			default:
				out = append(out, ProofFailure{
					StepID: st.ID, Kind: p.Kind, Target: p.Target,
					Reason: fmt.Sprintf("未知证据类型 %q（只支持 file/diff/test）", p.Kind),
				})
			}
		}
	}
	return out
}

func containsName(names []string, target string) bool {
	target = filepath.Clean(target)
	for _, n := range names {
		if filepath.Clean(n) == target {
			return true
		}
	}
	return false
}

// EvidenceDefects 把证据核验失败项转为缺陷条目（进入下一轮修复清单）。
func EvidenceDefects(failures []ProofFailure) []types.GoalDefect {
	out := make([]types.GoalDefect, 0, len(failures))
	for _, f := range failures {
		out = append(out, types.GoalDefect{
			ID:     fmt.Sprintf("d-evidence-%s", f.StepID),
			Desc:   fmt.Sprintf("证据核验未通过（步骤 %s）：%s", f.StepID, f.Reason),
			Status: "open",
		})
	}
	return out
}
