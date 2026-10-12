// Package eval 提供离线可复现的 agent 级评估 harness（对标前沿 OpenHands/Aider
// "把评估做成可复现一等公民"的零依赖落地）。
//
// 用法：编写 eval.yaml 定义任务集（prompt + gold/文件/命令检查器），
// 运行 `contextloom eval --file eval.yaml` 输出通过率报告。
// dry-run 模式只验证解析与检查器本身（不调用任何外部 API），可在 CI 复跑。
package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Spec 评估集定义（eval.yaml）。
type Spec struct {
	Name  string     `yaml:"name"`
	Tasks []TaskSpec `yaml:"tasks"`
}

// TaskSpec 单个评估任务。
type TaskSpec struct {
	ID           string      `yaml:"id"`
	Prompt       string      `yaml:"prompt"`
	Files        []string    `yaml:"files"`         // 从 spec 所在目录拷贝到临时工作区的文件（相对路径）
	GoldContains []string    `yaml:"gold_contains"` // agent 输出包含任一子串即过（不区分大小写）
	FileContains []FileCheck `yaml:"file_contains"` // 工作区文件包含子串（不区分大小写）
	CheckRegex   []string    `yaml:"check_regex"`   // agent 输出必须全部匹配的正则（Terminal-Bench regex test 映射；dry-run 不执行）
	Check        string      `yaml:"check"`         // 在工作区执行命令 exit 0 即过（dry-run 跳过）
}

// FileCheck 文件内容检查。
type FileCheck struct {
	File   string `yaml:"file"`
	Substr string `yaml:"substr"`
}

// TaskResult 单任务结果。
type TaskResult struct {
	ID     string `json:"id"`
	Pass   bool   `json:"pass"`
	Err    string `json:"err,omitempty"`
	Ms     int64  `json:"ms"`
	Output string `json:"output,omitempty"` // agent 输出片段（截断 300 字符）
}

// Report 评估报告。
type Report struct {
	Name     string       `json:"name"`
	Tasks    []TaskResult `json:"tasks"`
	Passed   int          `json:"passed"`
	Total    int          `json:"total"`
	PassRate float64      `json:"pass_rate"`
	DryRun   bool         `json:"dry_run"`
}

// Options Run 选项。
type Options struct {
	// DryRun 只验证解析/拷贝/检查器，不执行 agent（不调用外部 API）。
	DryRun bool
	// RunTask 执行 agent 任务（返回模型输出）。nil 且非 DryRun 时任务标记为 skip。
	RunTask func(ctx context.Context, workDir, prompt string) (string, error)
}

// LoadSpec 解析 eval.yaml。
func LoadSpec(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 eval 文件失败: %w", err)
	}
	var spec Spec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("解析 eval.yaml 失败: %w", err)
	}
	if spec.Name == "" {
		spec.Name = filepath.Base(path)
	}
	if len(spec.Tasks) == 0 {
		return nil, fmt.Errorf("eval.yaml 未定义任何任务（tasks 为空）")
	}
	for i := range spec.Tasks {
		t := &spec.Tasks[i]
		if t.ID == "" {
			t.ID = fmt.Sprintf("task-%d", i+1)
		}
		if t.Prompt == "" && len(t.FileContains) == 0 && t.Check == "" && len(t.CheckRegex) == 0 {
			return nil, fmt.Errorf("任务 %s: prompt 与检查器不能全空", t.ID)
		}
		// 解析校验：check_regex 必须可编译（非法正则直接报错，避免运行期 panic）
		for _, re := range t.CheckRegex {
			if _, err := regexp.Compile(re); err != nil {
				return nil, fmt.Errorf("任务 %s: check_regex %q 非法: %w", t.ID, re, err)
			}
		}
	}
	return &spec, nil
}

// Run 执行评估集，返回报告。临时工作区自动清理。
func Run(ctx context.Context, specPath string, opts Options) (*Report, error) {
	spec, err := LoadSpec(specPath)
	if err != nil {
		return nil, err
	}
	return RunSpec(ctx, spec, filepath.Dir(specPath), opts)
}

// RunSpec 执行已解析的评估集（spec），baseDir 用于解析 files 相对路径
// （拷贝任务文件的基准目录）。LoadTerminalBench 的产物可直接传入
// （baseDir 传 tasks.jsonl 所在目录），实现外部基准（Terminal-Bench）
// 与内部 eval.yaml 共用同一执行与判定引擎。
func RunSpec(ctx context.Context, spec *Spec, baseDir string, opts Options) (*Report, error) {
	rep := &Report{Name: spec.Name, DryRun: opts.DryRun, Total: len(spec.Tasks)}
	for i := range spec.Tasks {
		t := &spec.Tasks[i]
		start := time.Now()
		res := TaskResult{ID: t.ID}

		workDir, err := os.MkdirTemp("", "contextloom-eval-*")
		if err != nil {
			res.Err = "创建临时目录失败: " + err.Error()
			rep.Tasks = append(rep.Tasks, res)
			continue
		}
		// 拷贝任务文件（相对 spec 目录）
		copyErr := copyFiles(baseDir, workDir, t.Files)

		if copyErr == nil {
			if !opts.DryRun && opts.RunTask != nil {
				out, runErr := opts.RunTask(ctx, workDir, t.Prompt)
				if runErr != nil {
					res.Err = runErr.Error()
				} else {
					res.Output = truncate(out, 300)
					// 通过 = (gold 任一命中 或 gold 为空) AND (所有文件检查命中)
					//        AND (所有 check_regex 匹配 agent 输出)
					res.Pass = checkOutput(t, out) && checkFiles(t, workDir) && checkRegex(t, out)
					// check 命令检查器：gold/文件/正则通过后再执行命令（exit 0 即过）
					if res.Pass && strings.TrimSpace(t.Check) != "" {
						if err := CheckCommand(workDir, t.Check); err != nil {
							res.Err = "check 命令失败: " + err.Error()
							res.Pass = false
						}
					}
				}
			} else {
				// dry-run 或无执行器：只验证文件检查器（gold/check/check_regex
				// 均不执行，保持 dry-run 零副作用——不调用模型、不跑命令、不跑正则）
				res.Pass = checkFiles(t, workDir)
				if opts.DryRun {
					res.Output = "[dry-run] 未调用模型"
				}
			}
		} else {
			res.Err = copyErr.Error()
		}

		res.Ms = time.Since(start).Milliseconds()
		os.RemoveAll(workDir)

		if res.Pass {
			rep.Passed++
		}
		rep.Tasks = append(rep.Tasks, res)
	}
	rep.PassRate = float64(rep.Passed) / float64(rep.Total)
	return rep, nil
}

// checkOutput gold_contains 检查（任一命中即过，不区分大小写）。
func checkOutput(t *TaskSpec, output string) bool {
	if len(t.GoldContains) == 0 {
		return true
	}
	lo := strings.ToLower(output)
	for _, g := range t.GoldContains {
		if strings.Contains(lo, strings.ToLower(g)) {
			return true
		}
	}
	return false
}

// checkRegex check_regex 检查：所有正则必须全部匹配 agent 输出
// （Terminal-Bench regex test 语义——每个 test 都要过；空列表视为通过）。
// 正则已在 LoadSpec/LoadTerminalBench 解析时校验可编译，此处 MustCompile 安全。
func checkRegex(t *TaskSpec, output string) bool {
	for _, re := range t.CheckRegex {
		if !regexp.MustCompile(re).MatchString(output) {
			return false
		}
	}
	return true
}

// checkFiles file_contains 检查。
func checkFiles(t *TaskSpec, workDir string) bool {
	for _, fc := range t.FileContains {
		data, err := os.ReadFile(filepath.Join(workDir, fc.File))
		if err != nil {
			return false
		}
		if !strings.Contains(strings.ToLower(string(data)), strings.ToLower(fc.Substr)) {
			return false
		}
	}
	return true
}

// CheckCommand 执行任务命令检查器（在 workDir 中，exit 0 即过）。
func CheckCommand(workDir, cmdStr string) error {
	if strings.TrimSpace(cmdStr) == "" {
		return nil
	}
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "CONTEXTLOOM_EVAL=1")
	return cmd.Run()
}

func copyFiles(baseDir, workDir string, files []string) error {
	for _, f := range files {
		src := filepath.Join(baseDir, f)
		dst := filepath.Join(workDir, f)
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("拷贝任务文件 %s 失败: %w", f, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
