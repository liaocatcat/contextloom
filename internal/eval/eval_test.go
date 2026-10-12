package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEval(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "eval.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSpec_Valid(t *testing.T) {
	p := writeEval(t, `
name: 示例评估集
tasks:
  - id: t1
    prompt: "写一个 Go 函数"
    gold_contains: ["func add"]
  - id: t2
    prompt: "创建文件"
    files: ["main.go"]
    file_contains:
      - file: "main.go"
        substr: "func main"
    check: "test -f main.go"
`)
	spec, err := LoadSpec(p)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if spec.Name != "示例评估集" || len(spec.Tasks) != 2 {
		t.Fatalf("解析错误: %+v", spec)
	}
	if spec.Tasks[1].ID != "t2" || spec.Tasks[1].Files[0] != "main.go" {
		t.Fatalf("t2 解析错误: %+v", spec.Tasks[1])
	}
}

func TestLoadSpec_Errors(t *testing.T) {
	// 坏 yaml
	p := writeEval(t, "tasks: [unclosed")
	if _, err := LoadSpec(p); err == nil {
		t.Fatal("坏 yaml 应报错")
	}
	// 空任务
	p2 := writeEval(t, "name: x\ntasks: []")
	if _, err := LoadSpec(p2); err == nil {
		t.Fatal("空任务应报错")
	}
	// 缺 id 自动补
	p3 := writeEval(t, "name: x\ntasks:\n  - prompt: hi\n    gold_contains: [a]")
	spec, err := LoadSpec(p3)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Tasks[0].ID != "task-1" {
		t.Fatalf("缺 id 应自动补 task-1，got %q", spec.Tasks[0].ID)
	}
}

func TestCheckOutput(t *testing.T) {
	t1 := &TaskSpec{GoldContains: []string{"func add", "return a+b"}}
	if !checkOutput(t1, "package main\nfunc add(a,b int) int { return a+b }") {
		t.Fatal("应命中 func add")
	}
	if checkOutput(t1, "nothing here") {
		t.Fatal("不应命中")
	}
	// 空 gold → 通过
	if !checkOutput(&TaskSpec{}, "any") {
		t.Fatal("空 gold_contains 应通过")
	}
	// 大小写不敏感
	if !checkOutput(&TaskSpec{GoldContains: []string{"FUNC ADD"}}, "func add") {
		t.Fatal("大小写不敏感")
	}
}

func TestCheckFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc main() {}"), 0o644)
	t1 := &TaskSpec{FileContains: []FileCheck{{File: "a.go", Substr: "func main"}}}
	if !checkFiles(t1, dir) {
		t.Fatal("应命中文件内容")
	}
	t2 := &TaskSpec{FileContains: []FileCheck{{File: "a.go", Substr: "not exists"}}}
	if checkFiles(t2, dir) {
		t.Fatal("不应命中")
	}
	// 文件不存在
	t3 := &TaskSpec{FileContains: []FileCheck{{File: "nope.go", Substr: "x"}}}
	if checkFiles(t3, dir) {
		t.Fatal("文件不存在应失败")
	}
}

func TestRun_DryRun(t *testing.T) {
	p := writeEval(t, `
name: dry
tasks:
  - id: t1
    prompt: "改 main.go"
    files: ["main.go"]
    file_contains:
      - file: "main.go"
        substr: "func main"
    check: "echo skip"
`)
	// main.go 放在 eval.yaml 同目录（files 相对 spec 目录拷贝）
	os.WriteFile(filepath.Join(filepath.Dir(p), "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	// RunTask 为 nil + DryRun：不调用模型、不执行命令，只跑文件检查器
	rep, err := Run(context.Background(), p, Options{DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Tasks) != 1 || !rep.Tasks[0].Pass {
		t.Fatalf("dry-run t1 应通过（文件已含 substr），got %+v", rep.Tasks)
	}
	if !strings.Contains(rep.Tasks[0].Output, "dry-run") {
		t.Fatalf("dry-run 输出应标注，got %q", rep.Tasks[0].Output)
	}
	// 临时目录应已清理
	leftovers, _ := filepath.Glob(filepath.Join(os.TempDir(), "contextloom-eval-*"))
	for _, l := range leftovers {
		if strings.Contains(l, "contextloom-eval-") {
			t.Fatalf("临时目录未清理: %s", l)
		}
	}
}

func TestRun_WithRunTask(t *testing.T) {
	p := writeEval(t, `
name: real
tasks:
  - id: pass
    prompt: "任务"
    gold_contains: ["DONE"]
  - id: fail
    prompt: "任务2"
    gold_contains: ["NEVER"]
  - id: cmd
    prompt: "任务3"
    files: ["main.go"]
    check: "grep -q 'package main' main.go"
`)
	// main.go 放在 eval.yaml 同目录（files 相对 spec 目录拷贝）
	os.WriteFile(filepath.Join(filepath.Dir(p), "main.go"), []byte("package main\n"), 0o644)
	rep, err := Run(context.Background(), p, Options{
		RunTask: func(ctx context.Context, workDir, prompt string) (string, error) {
			switch prompt {
			case "任务":
				return "all DONE", nil
			case "任务3":
				return "output", nil
			default:
				return "bad", nil
			}
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Passed != 2 || rep.Total != 3 {
		t.Fatalf("应 2/3 通过，got %+v", rep)
	}
	if rep.PassRate != 2.0/3.0 {
		t.Fatalf("通过率错误: %v", rep.PassRate)
	}
	if rep.Tasks[1].ID != "fail" || rep.Tasks[1].Pass {
		t.Fatalf("fail 任务应失败: %+v", rep.Tasks[1])
	}
}

func TestRun_MissingTaskFile(t *testing.T) {
	p := writeEval(t, `
name: missing
tasks:
  - id: t1
    prompt: "x"
    files: ["nope.go"]
    file_contains: [{file: "nope.go", substr: "x"}]
`)
	rep, err := Run(context.Background(), p, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tasks[0].Pass {
		t.Fatalf("缺失任务文件应失败: %+v", rep.Tasks[0])
	}
	if !strings.Contains(rep.Tasks[0].Err, "拷贝任务文件") {
		t.Fatalf("错误信息应说明拷贝失败: %q", rep.Tasks[0].Err)
	}
}
