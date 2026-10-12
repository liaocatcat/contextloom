package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleTasksJSONL 样例 tasks.jsonl：≥2 任务，
// 一个含 setup+command+regex test，一个含 file test，另加容错/多正则用例。
const sampleTasksJSONL = `{"id":"tb1","name":"echo-hello","detail":"输出 hello 并准备目录","setup":[{"type":"shell","cmd":"mkdir -p work"}],"command":"echo hello","tests":[{"type":"regex","pattern":"hello"}]}
{"id":"tb2","name":"create-file","detail":"创建 demo.txt","setup":[],"command":"","tests":[{"type":"file","path":"demo.txt","contents":"hello world"}]}
{"id":"tb3","name":"skip-unknown","detail":"未知类型容错跳过","setup":[{"type":"pip","cmd":"ignored"}],"command":"","tests":[{"type":"nonsense","pattern":"x"}]}
{"id":"tbmulti","name":"multi-regex","detail":"多正则需全匹配","setup":[],"command":"","tests":[{"type":"regex","pattern":"hello"},{"type":"regex","pattern":"world"}]}
`

func writeTasksJSONL(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "tasks.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTerminalBench_File(t *testing.T) {
	p := writeTasksJSONL(t, sampleTasksJSONL)
	spec, err := LoadTerminalBench(p)
	if err != nil {
		t.Fatalf("LoadTerminalBench: %v", err)
	}
	if len(spec.Tasks) != 4 {
		t.Fatalf("应有 4 个任务, got %d", len(spec.Tasks))
	}
	if spec.Name != "terminal-bench" {
		t.Errorf("Spec.Name 应为 terminal-bench, got %q", spec.Name)
	}

	// tb1: setup shell + command 用 && 拼接 → Check；regex test → CheckRegex
	t1 := spec.Tasks[0]
	if t1.ID != "tb1" {
		t.Errorf("ID 应为 tb1, got %q", t1.ID)
	}
	if !strings.Contains(t1.Check, "mkdir -p work") || !strings.Contains(t1.Check, "echo hello") ||
		!strings.Contains(t1.Check, " && ") {
		t.Errorf("Check 应含 setup && command, got %q", t1.Check)
	}
	if len(t1.CheckRegex) != 1 || t1.CheckRegex[0] != "hello" {
		t.Errorf("CheckRegex 解析错误: %+v", t1.CheckRegex)
	}
	if t1.Prompt != "输出 hello 并准备目录" {
		t.Errorf("Prompt 应取 detail, got %q", t1.Prompt)
	}

	// tb2: file test → FileContains；无 command/setup → Check 为空
	t2 := spec.Tasks[1]
	if t2.ID != "tb2" || t2.Check != "" {
		t.Errorf("tb2 Check 应为空, got %q", t2.Check)
	}
	if len(t2.FileContains) != 1 || t2.FileContains[0].File != "demo.txt" ||
		t2.FileContains[0].Substr != "hello world" {
		t.Errorf("FileContains 解析错误: %+v", t2.FileContains)
	}

	// tb3: 未知 setup/test type 容错跳过（不报错、不产生检查器）
	t3 := spec.Tasks[2]
	if t3.Check != "" || len(t3.CheckRegex) != 0 || len(t3.FileContains) != 0 {
		t.Errorf("未知类型应跳过: %+v", t3)
	}

	// tbmulti: 多正则全部进入 CheckRegex
	t4 := spec.Tasks[3]
	if len(t4.CheckRegex) != 2 || t4.CheckRegex[0] != "hello" || t4.CheckRegex[1] != "world" {
		t.Errorf("多正则解析错误: %+v", t4.CheckRegex)
	}
}

func TestLoadTerminalBench_Dir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tasks.jsonl"), []byte(sampleTasksJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := LoadTerminalBench(dir)
	if err != nil {
		t.Fatalf("目录模式: %v", err)
	}
	if len(spec.Tasks) != 4 {
		t.Fatalf("目录模式应解析 4 任务, got %d", len(spec.Tasks))
	}
}

func TestLoadTerminalBench_Errors(t *testing.T) {
	// 路径不存在
	if _, err := LoadTerminalBench("/nonexistent/tasks.jsonl"); err == nil {
		t.Fatal("不存在路径应报错")
	}
	// 目录无 tasks.jsonl
	if _, err := LoadTerminalBench(t.TempDir()); err == nil {
		t.Fatal("空目录应报错")
	}
	// 非法 JSON 行
	p := writeTasksJSONL(t, "{bad json}\n")
	if _, err := LoadTerminalBench(p); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
	// 空文件
	p2 := writeTasksJSONL(t, "")
	if _, err := LoadTerminalBench(p2); err == nil {
		t.Fatal("空 tasks.jsonl 应报错")
	}
}

func TestLoadTerminalBench_InvalidRegex(t *testing.T) {
	p := writeTasksJSONL(t, `{"id":"bad","detail":"x","tests":[{"type":"regex","pattern":"[unclosed"}]}
`)
	if _, err := LoadTerminalBench(p); err == nil {
		t.Fatal("非法正则应报错")
	}
}

func TestLoadTerminalBench_GeneratedID(t *testing.T) {
	p := writeTasksJSONL(t, `{"detail":"no id no name","tests":[]}
`)
	spec, err := LoadTerminalBench(p)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Tasks[0].ID == "" || !strings.HasPrefix(spec.Tasks[0].ID, "tbtask-") {
		t.Fatalf("空 id/name 应生成 tbtask-N ID, got %q", spec.Tasks[0].ID)
	}
}

// TestTerminalBench_DryRun LoadTerminalBench 产物 dry-run：
// 不调用模型、不执行 Check 命令、不跑正则（零副作用），仅文件检查器生效。
func TestTerminalBench_DryRun(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tasks.jsonl")
	if err := os.WriteFile(p, []byte(sampleTasksJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadTerminalBench(p)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := RunSpec(context.Background(), spec, dir, Options{DryRun: true})
	if err != nil {
		t.Fatalf("RunSpec: %v", err)
	}
	if rep.Total != 4 {
		t.Fatalf("应有 4 任务: %+v", rep)
	}
	// dry-run 语义：
	//   - tb1/tb3/tbmulti 无文件检查器 → 通过（不执行 Check 命令、不跑正则）
	//   - tb2 的 file test 依赖 agent 执行产生的 demo.txt，dry-run 不执行 agent
	//     → 检查器如实判定失败（验证"文件检查器真实生效"而非"假通过"）
	if !rep.Tasks[0].Pass || !rep.Tasks[2].Pass || !rep.Tasks[3].Pass {
		t.Errorf("tb1/tb3/tbmulti 应通过: %+v", rep.Tasks)
	}
	if rep.Tasks[1].Pass {
		t.Errorf("tb2 dry-run 应失败（文件未产生，检查器如实判定）: %+v", rep.Tasks[1])
	}
	// 所有任务输出标注 dry-run，证明未调用模型
	for _, r := range rep.Tasks {
		if !strings.Contains(r.Output, "dry-run") {
			t.Errorf("dry-run 输出应标注: %+v", r)
		}
	}
	// 证明未执行 Check 命令：tb1 的 check 含 mkdir -p work，dry-run 后
	// 不产生任何 work 目录副作用（临时工作区已清理，此处以输出标注佐证）
	if rep.Tasks[0].Output != "[dry-run] 未调用模型" {
		t.Errorf("dry-run 不应调用模型: %+v", rep.Tasks[0])
	}
}

// TestTerminalBench_RealRun 真实路径：
// RunTask 假函数 + 真实 Check 命令（test -f）+ 正则匹配/不匹配两分支。
func TestTerminalBench_RealRun(t *testing.T) {
	dir := t.TempDir()
	content := `{"id":"tbregex","detail":"输出 hello","setup":[],"command":"echo hello","tests":[{"type":"regex","pattern":"hello"}]}
{"id":"tbmultifail","detail":"多正则缺一","setup":[],"command":"","tests":[{"type":"regex","pattern":"hello"},{"type":"regex","pattern":"world"}]}
{"id":"tbcheck","detail":"创建 demo.txt","setup":[{"type":"shell","cmd":"test -f demo.txt"}],"command":"","tests":[{"type":"file","path":"demo.txt","contents":"demo"}]}
{"id":"tbcheckfail","detail":"check 失败","setup":[{"type":"shell","cmd":"test -f nope.txt"}],"command":"","tests":[]}
`
	p := filepath.Join(dir, "tasks.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadTerminalBench(p)
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	rep, err := RunSpec(context.Background(), spec, dir, Options{
		RunTask: func(ctx context.Context, workDir, prompt string) (string, error) {
			calls++
			switch {
			case strings.Contains(prompt, "输出 hello"):
				// 匹配正则 → tbregex 过
				return "result: hello", nil
			case strings.Contains(prompt, "多正则缺一"):
				// 只匹配一个正则 → tbmultifail 不过（全匹配语义）
				return "hello only", nil
			case strings.Contains(prompt, "创建 demo.txt"):
				// 模拟 agent 行为：在工作区创建文件，随后真实 Check 命令验证
				if err := os.WriteFile(filepath.Join(workDir, "demo.txt"), []byte("demo content"), 0o644); err != nil {
					return "", err
				}
				return "created", nil
			default:
				return "nothing", nil
			}
		},
	})
	if err != nil {
		t.Fatalf("RunSpec: %v", err)
	}
	if calls != 4 {
		t.Fatalf("RunTask 应被调用 4 次, got %d", calls)
	}

	byID := map[string]*TaskResult{}
	for i := range rep.Tasks {
		byID[rep.Tasks[i].ID] = &rep.Tasks[i]
	}

	// tbregex: 正则匹配 + Check "echo hello" exit 0 → 通过
	if !byID["tbregex"].Pass {
		t.Errorf("tbregex 应通过（正则命中+check 成功）: %+v", byID["tbregex"])
	}
	// tbmultifail: 两个正则只命中一个 → 失败（全匹配语义）
	if byID["tbmultifail"].Pass {
		t.Errorf("tbmultifail 应失败（多正则须全匹配）: %+v", byID["tbmultifail"])
	}
	// tbcheck: 文件命中 + 真实 check 命令 test -f demo.txt 成功 → 通过
	if !byID["tbcheck"].Pass {
		t.Errorf("tbcheck 应通过（文件命中+check 命令成功）: %+v", byID["tbcheck"])
	}
	// tbcheckfail: check 命令 test -f nope.txt 失败 → 失败
	if byID["tbcheckfail"].Pass {
		t.Errorf("tbcheckfail 应失败（check 命令 exit 非 0）: %+v", byID["tbcheckfail"])
	}
	if !strings.Contains(byID["tbcheckfail"].Err, "check 命令失败") {
		t.Errorf("tbcheckfail 错误信息应说明 check 失败: %q", byID["tbcheckfail"].Err)
	}
}
