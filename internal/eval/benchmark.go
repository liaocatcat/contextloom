// Terminal-Bench 基准适配器：把 Terminal-Bench 官方 tasks.jsonl 转换为
// 内部 eval.Spec，复用现有离线评估 harness（eval.yaml 检查器体系）。
//
// 对标前沿（OpenHands eval 一等公民 / Aider 每周公开跑分）的零依赖落地：
// 直接消费 Terminal-Bench 社区发布的任务集，`contextloom eval` 即可跑分，
// 弥补"无外部基准集成"缺陷——无需新增任何依赖、全平台可交叉编译。
//
// Terminal-Bench 单任务行格式（tasks.jsonl）：
//
//	{
//	  "id": "t1", "name": "echo", "detail": "任务描述",
//	  "setup": [{"type": "shell", "cmd": "mkdir -p work"}],
//	  "command": "echo hello",
//	  "tests": [
//	    {"type": "regex", "pattern": "hello"},
//	    {"type": "file", "path": "demo.txt", "contents": "hello world"}
//	  ]
//	}
//
// 映射规则：
//   - id/name → TaskSpec.ID（都为空则按行号生成）；
//   - detail → TaskSpec.Prompt（任务描述）；
//   - setup 中 type=shell 的 cmd 与 command 用 " && " 拼接 → TaskSpec.Check
//     （Terminal-Bench 语义：先 setup 后执行 command，任一失败即任务失败）；
//   - tests 中 type=file → FileContains{File,Substr}；
//   - tests 中 type=regex → TaskSpec.CheckRegex（全部须匹配 agent 输出）；
//   - 未知 setup/test type 容错跳过（不报错，保证向前兼容新版本格式）。
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TerminalBenchTask tasks.jsonl 单行（仅声明本适配器消费的字段，
// 其余字段忽略——Terminal-Bench 官方 schema 之外的扩展字段不受影响）。
type TerminalBenchTask struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Detail  string               `json:"detail"`
	Setup   []TerminalBenchSetup `json:"setup"`
	Command string               `json:"command"`
	Tests   []TerminalBenchTest  `json:"tests"`
}

// TerminalBenchSetup 任务准备步骤（只处理 type=shell）。
type TerminalBenchSetup struct {
	Type string `json:"type"`
	Cmd  string `json:"cmd"`
}

// TerminalBenchTest 任务检查器（type=file / type=regex）。
type TerminalBenchTest struct {
	Type     string `json:"type"`
	Pattern  string `json:"pattern"`
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

// LoadTerminalBench 解析 Terminal-Bench 格式的 tasks.jsonl 为内部 Spec。
// path 可以是 tasks.jsonl 文件本身，或包含 tasks.jsonl 的目录。
// 返回的 Spec 可直接传给 RunSpec 执行（baseDir 传 tasks.jsonl 所在目录，
// 使 file test 的相对路径与真实工作区一致）。
func LoadTerminalBench(path string) (*Spec, error) {
	// 定位 tasks.jsonl：文件本身，或目录下的 tasks.jsonl
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("Terminal-Bench 路径不可访问: %w", err)
	}
	file := path
	if info.IsDir() {
		file = filepath.Join(path, "tasks.jsonl")
		if _, err := os.Stat(file); err != nil {
			return nil, fmt.Errorf("目录中未找到 tasks.jsonl: %w", err)
		}
	}

	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("打开 tasks.jsonl 失败: %w", err)
	}
	defer f.Close()

	spec := &Spec{Name: "terminal-bench"}
	sc := bufio.NewScanner(f)
	// 单行任务可能很大（detail/tests 超长），放宽缓冲区上限
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var tb TerminalBenchTask
		if err := json.Unmarshal([]byte(line), &tb); err != nil {
			return nil, fmt.Errorf("tasks.jsonl 第 %d 行解析失败: %w", lineNo, err)
		}
		ts, err := tb.toTaskSpec(lineNo)
		if err != nil {
			return nil, err
		}
		spec.Tasks = append(spec.Tasks, *ts)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 tasks.jsonl 失败: %w", err)
	}
	if len(spec.Tasks) == 0 {
		return nil, fmt.Errorf("tasks.jsonl 未定义任何任务")
	}
	return spec, nil
}

// toTaskSpec 单任务行 → 内部 TaskSpec（含正则可编译校验）。
func (tb *TerminalBenchTask) toTaskSpec(lineNo int) (*TaskSpec, error) {
	ts := &TaskSpec{}

	// id/name → ID；都为空则按行号生成（保证报告可追溯）
	switch {
	case tb.ID != "":
		ts.ID = tb.ID
	case tb.Name != "":
		ts.ID = tb.Name
	default:
		ts.ID = fmt.Sprintf("tbtask-%d", lineNo)
	}

	// detail → Prompt（Terminal-Bench 无独立 prompt 字段）
	ts.Prompt = tb.Detail
	if ts.Prompt == "" {
		ts.Prompt = fmt.Sprintf("Task: %s", ts.ID)
	}

	// setup(shell) 与 command 用 " && " 拼接 → Check：
	// 语义与 Terminal-Bench 一致——先准备环境，再执行主命令，任一失败即任务失败。
	var parts []string
	for _, s := range tb.Setup {
		if s.Type == "shell" && strings.TrimSpace(s.Cmd) != "" {
			parts = append(parts, s.Cmd)
		}
		// 未知 setup type（如 pip/apt 等未来扩展）容错跳过
	}
	if strings.TrimSpace(tb.Command) != "" {
		parts = append(parts, tb.Command)
	}
	if len(parts) > 0 {
		ts.Check = strings.Join(parts, " && ")
	}
	// 无 command 且无 setup → Check 为空（仅文件/正则检查）

	// tests 映射
	for _, t := range tb.Tests {
		switch t.Type {
		case "file":
			ts.FileContains = append(ts.FileContains, FileCheck{File: t.Path, Substr: t.Contents})
		case "regex":
			ts.CheckRegex = append(ts.CheckRegex, t.Pattern)
		default:
			// 未知 test type 容错跳过（不报错）
		}
	}

	// 解析校验：所有 CheckRegex 必须可编译（regexp.Compile），非法返回错误
	for _, re := range ts.CheckRegex {
		if _, err := regexp.Compile(re); err != nil {
			return nil, fmt.Errorf("任务 %s 的正则 test 非法: %q: %w", ts.ID, re, err)
		}
	}
	return ts, nil
}
