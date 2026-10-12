package toolchain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ① 环检测与缺失依赖报错
// ---------------------------------------------------------------------------

func TestValidateCycleAndMissingDep(t *testing.T) {
	// 环：a -> b -> a
	cyc := Toolchain{
		Name: "cyc",
		Steps: []Step{
			{ID: "a", Type: StepTypeTool, Tool: "x", DependsOn: Depends{"b"}},
			{ID: "b", Type: StepTypeTool, Tool: "x", DependsOn: Depends{"a"}},
		},
	}
	errs := Validate(cyc, ValidateOptions{})
	if !contains(errs, "cycle") {
		t.Fatalf("expected cycle error, got %v", errs)
	}

	// 缺失依赖：c 依赖不存在的步骤。
	miss := Toolchain{
		Name: "miss",
		Steps: []Step{
			{ID: "a", Type: StepTypeTool, Tool: "x"},
			{ID: "b", Type: StepTypeTool, Tool: "x", DependsOn: Depends{"ghost"}},
		},
	}
	errs = Validate(miss, ValidateOptions{})
	if !contains(errs, "depends_on") {
		t.Fatalf("expected missing-dep error, got %v", errs)
	}
}

func TestValidateOtherErrors(t *testing.T) {
	// 未知步骤类型 / id 重复 / 未知工具 / id 含点 / retry 非法。
	c := Toolchain{
		Name: "x",
		Steps: []Step{
			{ID: "a", Type: "bogus"},
			{ID: "a", Type: StepTypeTool, Tool: "t"},
			{ID: "c.d", Type: StepTypeTool, Tool: "t"},
			{ID: "e", Type: StepTypeTool, Tool: "unknown_tool"},
			{ID: "f", Type: StepTypeTool, Tool: "t", Retry: -1},
			{ID: "g", Type: StepTypeTool},
			{ID: "h", Type: StepTypeLLM},
			{ID: "i", Type: StepTypeRef},
		},
	}
	errs := Validate(c, ValidateOptions{KnownTools: map[string]bool{"t": true}})
	for _, want := range []string{"unknown step type", "duplicate step id", "must not contain '.'", "unknown tool", "retry must be >= 0", "tool step requires", "llm step requires", "ref step requires"} {
		if !contains(errs, want) {
			t.Errorf("expected validation to cover %q, got %v", want, errs)
		}
	}
}

func TestValidateSubChain(t *testing.T) {
	// ref 类步骤引用未注入的子链名。
	c := Toolchain{
		Name: "x",
		Steps: []Step{
			{ID: "a", Type: StepTypeRef, Ref: "nope-chain"},
		},
	}
	errs := Validate(c, ValidateOptions{KnownChains: map[string]bool{"yes-chain": true}})
	if !contains(errs, "unknown sub-chain") {
		t.Fatalf("expected unknown sub-chain error, got %v", errs)
	}
}

// ---------------------------------------------------------------------------
// ② $ref 产物传递与字段级提取
// ---------------------------------------------------------------------------

func TestRefForwardingAndFieldExtraction(t *testing.T) {
	var tools, llms []string
	chain := Toolchain{
		Name: "ch",
		Steps: []Step{
			{
				ID: "gen", Type: StepTypeTool, Tool: "diff",
				Params: map[string]any{"path": "src/"},
			},
			{
				ID: "review", Type: StepTypeLLM,
				Prompt: "审查 $ref: gen.output，参考 $gen.path",
			},
			{
				ID: "report", Type: StepTypeTool, Tool: "write_file",
				Params: map[string]any{"content": "$ref: review.report"},
			},
		},
	}
	table := map[string]ToolExecutor{
		"diff": func(name string, params map[string]any) (string, error) {
			tools = append(tools, name)
			return "output: diff-body\npath: src/\nlines: 42", nil
		},
		"write_file": func(name string, params map[string]any) (string, error) {
			tools = append(tools, name)
			c, _ := params["content"].(string)
			return "written: " + c, nil
		},
	}
	opts := RunOptions{
		Tools: func(name string, params map[string]any) (string, error) {
			return table[name](name, params)
		},
		LLM: func(ctx context.Context, prompt string, artifacts map[string]string) (string, error) {
			llms = append(llms, prompt)
			// 断言 prompt 已解析出引用值。
			if !strings.Contains(prompt, "diff-body") || !strings.Contains(prompt, "src/") {
				t.Errorf("prompt did not resolve refs: %q", prompt)
			}
			return "report: 审查通过\nscore: 95", nil
		},
		MaxFanout: 4,
	}
	res, err := Run(context.Background(), chain, opts)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if res.Artifacts["gen.output"] != "diff-body" {
		t.Errorf("gen.output = %q", res.Artifacts["gen.output"])
	}
	if res.Artifacts["review.report"] != "审查通过" {
		t.Errorf("review.report = %q", res.Artifacts["review.report"])
	}
	if res.Artifacts["report"] != "written: 审查通过" {
		t.Errorf("report = %q", res.Artifacts["report"])
	}
	// 依赖顺序保证：gen -> review -> report。
	if len(tools) != 2 || tools[0] != "diff" || tools[1] != "write_file" {
		t.Errorf("tool order = %v, want [diff write_file]", tools)
	}
}

// ---------------------------------------------------------------------------
// ③ 无依赖步骤并行执行（MaxFanout 生效，耗时断言）
// ---------------------------------------------------------------------------

func TestParallelMaxFanout(t *testing.T) {
	var active, maxActive int32
	slp := func(d time.Duration) ToolExecutor {
		return func(name string, params map[string]any) (string, error) {
			cur := atomic.AddInt32(&active, 1)
			for {
				old := atomic.LoadInt32(&maxActive)
				if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
					break
				}
			}
			time.Sleep(d)
			atomic.AddInt32(&active, -1)
			return name, nil
		}
	}
	chain := Toolchain{Name: "p"}
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		chain.Steps = append(chain.Steps, Step{ID: id, Type: StepTypeTool, Tool: id, DependsOn: Depends{}})
	}
	opts := RunOptions{Tools: slp(80 * time.Millisecond), MaxFanout: 2}
	start := time.Now()
	if _, err := Run(context.Background(), chain, opts); err != nil {
		t.Fatalf("run error: %v", err)
	}
	elapsed := time.Since(start)
	if maxActive > 2 {
		t.Errorf("maxActive = %d, want <= 2", maxActive)
	}
	if maxActive < 2 {
		t.Errorf("maxActive = %d, want >= 2 (parallelism not achieved)", maxActive)
	}
	// 6 步 / fanout 2 = 3 批 × 80ms ≈ 240ms；串行需 480ms。
	if elapsed >= 400*time.Millisecond {
		t.Errorf("elapsed = %v, want < 400ms (serial would be ~480ms)", elapsed)
	}
}

func TestDefaultFanout(t *testing.T) {
	chain := Toolchain{Name: "p"}
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		chain.Steps = append(chain.Steps, Step{ID: id, Type: StepTypeTool, Tool: id, DependsOn: Depends{}})
	}
	slp := func(name string, params map[string]any) (string, error) {
		time.Sleep(60 * time.Millisecond)
		return name, nil
	}
	start := time.Now()
	if _, err := Run(context.Background(), chain, RunOptions{Tools: slp}); err != nil {
		t.Fatalf("run error: %v", err)
	}
	// MaxFanout 缺省 4 → 4 步一轮 ≈ 60ms；串行 240ms。
	if elapsed := time.Since(start); elapsed >= 180*time.Millisecond {
		t.Errorf("elapsed = %v, want < 180ms (default fanout 4)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// ④ 失败步骤编号可读 + 未执行步骤列表
// ---------------------------------------------------------------------------

func TestFailureReadableAndSkipped(t *testing.T) {
	chain := Toolchain{
		Name: "f",
		Steps: []Step{
			{ID: "a", Type: StepTypeTool, Tool: "ok1"},
			{ID: "b", Type: StepTypeTool, Tool: "boom"},
			{ID: "c", Type: StepTypeTool, Tool: "ok2"},                          // 依赖 b（缺省全部前置）
			{ID: "d", Type: StepTypeTool, Tool: "ok3", DependsOn: Depends{"a"}}, // 独立分支，应继续执行
		},
	}
	ok := func(name string, params map[string]any) (string, error) { return name, nil }
	opts := RunOptions{
		Tools: func(name string, params map[string]any) (string, error) {
			if name == "boom" {
				return "", fmt.Errorf("boom")
			}
			return ok(name, params)
		},
	}
	res, err := Run(context.Background(), chain, opts)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `step[2] "b" failed: boom`) {
		t.Errorf("error not readable-numbered: %v", err)
	}
	if !strings.Contains(err.Error(), "unexecuted steps: [c]") {
		t.Errorf("error missing unexecuted list: %v", err)
	}
	if res.FailedStepID != "b" {
		t.Errorf("FailedStepID = %q, want b", res.FailedStepID)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "c" {
		t.Errorf("Skipped = %v, want [c]", res.Skipped)
	}
	// d（独立分支）应成功。
	if _, ok := res.Artifacts["d"]; !ok {
		t.Errorf("independent step d should have run, artifacts keys=%v", res.Artifacts)
	}
}

// ---------------------------------------------------------------------------
// ⑤ retry 生效
// ---------------------------------------------------------------------------

func TestRetrySucceedsAfterFails(t *testing.T) {
	var calls int32
	chain := Toolchain{
		Name: "r",
		Steps: []Step{
			{ID: "a", Type: StepTypeTool, Tool: "flaky", Retry: 3},
		},
	}
	opts := RunOptions{
		Tools: func(name string, params map[string]any) (string, error) {
			n := atomic.AddInt32(&calls, 1)
			if n < 3 {
				return "", fmt.Errorf("transient %d", n)
			}
			return "ok", nil
		},
	}
	res, err := Run(context.Background(), chain, opts)
	if err != nil {
		t.Fatalf("expected success after retries, got %v (calls=%d)", err, calls)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if res.Log[0].Attempts != 3 {
		t.Errorf("attempts = %d, want 3", res.Log[0].Attempts)
	}
	if res.Artifacts["a"] != "ok" {
		t.Errorf("artifact a = %q", res.Artifacts["a"])
	}
}

func TestRetryDefaultIsOne(t *testing.T) {
	var calls int32
	chain := Toolchain{
		Name:  "r",
		Steps: []Step{{ID: "a", Type: StepTypeTool, Tool: "x"}}, // Retry 缺省 0 → 1 次
	}
	opts := RunOptions{
		Tools: func(name string, params map[string]any) (string, error) {
			atomic.AddInt32(&calls, 1)
			return "", fmt.Errorf("always fail")
		},
	}
	res, err := Run(context.Background(), chain, opts)
	if err == nil {
		t.Fatal("expected failure")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry by default)", calls)
	}
	if res.Log[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", res.Log[0].Attempts)
	}
}

// ---------------------------------------------------------------------------
// ⑥ 嵌套子链引用
// ---------------------------------------------------------------------------

func TestNestedSubChain(t *testing.T) {
	sub := Toolchain{
		Name: "collector",
		Steps: []Step{
			{ID: "gather", Type: StepTypeTool, Tool: "git_log"},
			{ID: "summ", Type: StepTypeLLM, Prompt: "基于 $ref: gather 总结"},
		},
	}
	parent := Toolchain{
		Name: "p",
		Steps: []Step{
			{ID: "prep", Type: StepTypeTool, Tool: "setup"},
			{ID: "main", Type: StepTypeRef, Ref: "collector"},
		},
	}
	opts := RunOptions{
		Tools: func(name string, params map[string]any) (string, error) {
			switch name {
			case "setup":
				return "env: ready\n", nil
			case "git_log":
				return "commits: 5\n", nil
			}
			return "", fmt.Errorf("unknown %q", name)
		},
		LLM: func(ctx context.Context, prompt string, artifacts map[string]string) (string, error) {
			return "report: 周报\n", nil
		},
		SubChains: map[string]*Toolchain{"collector": &sub},
	}
	res, err := Run(context.Background(), parent, opts)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	// 子链产物以 "main.<key>" 前缀并入。
	if got := res.Artifacts["main.gather"]; !strings.Contains(got, "commits: 5") {
		t.Errorf("main.gather = %q, want commits", got)
	}
	if got := res.Artifacts["main.summ.report"]; got != "周报" {
		t.Errorf("main.summ.report = %q, want 周报", got)
	}
	// 子链整体产物（ref 步骤产物）注册为 "main"。
	if got := res.Artifacts["main"]; !strings.Contains(got, "commits") {
		t.Errorf("main = %q, want summary containing commits", got)
	}
}

// ---------------------------------------------------------------------------
// ⑦ yaml 反序列化往返
// ---------------------------------------------------------------------------

func TestYAMLRoundTrip(t *testing.T) {
	c := Toolchain{
		Name:    "rt",
		Version: "1.0.0",
		Params:  []Param{{Name: "p", Required: true, Desc: "d"}},
		Steps: []Step{
			{ID: "a", Type: StepTypeTool, Tool: "t1", Params: map[string]any{"x": "$ref: b.y"}},
			{ID: "b", Type: StepTypeLLM, Prompt: "hi $ref: a", DependsOn: Depends{}}, // 显式无依赖
			{ID: "c", Type: StepTypeRef, Ref: "subchain"},
		},
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Toolchain
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Steps) != 3 {
		t.Fatalf("steps = %d", len(back.Steps))
	}
	// depends_on 语义往返保持：b 显式无依赖（非 nil 空 Depends），a 缺省（nil）。
	if back.Steps[1].DependsOn == nil {
		t.Errorf("step b DependsOn should be non-nil (explicit empty), got nil")
	}
	if back.Steps[1].DependsOn != nil && len(back.Steps[1].DependsOn) != 0 {
		t.Errorf("step b DependsOn = %v, want empty non-nil", back.Steps[1].DependsOn)
	}
	if back.Steps[0].DependsOn != nil {
		t.Errorf("step a DependsOn should be nil (default), got %v", back.Steps[0].DependsOn)
	}
	if back.Steps[0].Params["x"] != "$ref: b.y" {
		t.Errorf("params lost: %v", back.Steps[0].Params)
	}
}

func TestLoadFromDir(t *testing.T) {
	dir := t.TempDir()
	name := "my-chain"
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	yamlText := `name: my-chain
version: 1.0.0
steps:
  - { id: a, tool: git_diff, args: {} }
  - { id: b, llm: { role_hint: x, prompt: "hi $ref: a" } }
`
	if err := os.WriteFile(filepath.Join(dir, name, "toolchain.yaml"), []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir, name)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Name != name || len(c.Steps) != 2 {
		t.Fatalf("loaded = %+v", c)
	}
	// 官方格式解析断言：tool+args / 嵌套 llm。
	s0 := c.Steps[0]
	if s0.Tool != "git_diff" || s0.InferredType() != StepTypeTool {
		t.Errorf("step a = %+v, want tool git_diff", s0)
	}
	s1 := c.Steps[1]
	if s1.InferredType() != StepTypeLLM || s1.RoleHint != "x" || !strings.Contains(s1.Prompt, "$ref: a") {
		t.Errorf("step b = %+v, want llm with role_hint x and $ref: a", s1)
	}
}

// TestSchemeDocFormat 验证方案 §4.3.1 官方 YAML 形态可完整解析并可执行。
func TestSchemeDocFormat(t *testing.T) {
	text := `name: code-review-chain
version: 1.0.0
params:
  - { name: path, required: true, desc: 审查目标路径 }
steps:
  - { id: diff,    tool: git_diff, args: { path: "{{param.path}}" } }
  - { id: analyze, llm: { role_hint: "代码审查专家", prompt: "审查 $ref: diff" } }
  - { id: report,  tool: write_file, args: { path: "review-{{ts}}.md", content: "$ref: analyze.report" } }
  - { id: publish, toolchain: weekly-report-chain }`
	c, err := ParseYAML([]byte(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if errs := Validate(*c, ValidateOptions{}); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	if c.Steps[0].InferredType() != StepTypeTool || c.Steps[0].Tool != "git_diff" {
		t.Errorf("diff = %+v", c.Steps[0])
	}
	if got := c.Steps[0].Params["path"]; got != "{{param.path}}" {
		t.Errorf("diff path = %v", got)
	}
	if c.Steps[1].InferredType() != StepTypeLLM || c.Steps[1].RoleHint != "代码审查专家" {
		t.Errorf("analyze = %+v", c.Steps[1])
	}
	if c.Steps[3].InferredType() != StepTypeRef || c.Steps[3].Ref != "weekly-report-chain" {
		t.Errorf("publish = %+v, want ref weekly-report-chain", c.Steps[3])
	}
	// 执行验证：注入 mock 工具/LLM 与子链，全链跑通。
	sub := Toolchain{
		Name: "weekly-report-chain",
		Steps: []Step{
			{ID: "collect", Type: StepTypeTool, Tool: "git_log"},
		},
	}
	var prompts []string
	opts := RunOptions{
		Tools: func(name string, params map[string]any) (string, error) {
			switch name {
			case "git_diff":
				return "diff-body", nil
			case "write_file":
				return "saved", nil
			case "git_log":
				return "log-body", nil
			}
			return "", fmt.Errorf("unknown tool %q", name)
		},
		LLM: func(ctx context.Context, prompt string, artifacts map[string]string) (string, error) {
			prompts = append(prompts, prompt)
			return "report: 通过\nscore: 90", nil
		},
		SubChains: map[string]*Toolchain{"weekly-report-chain": &sub},
		ParamsRun: map[string]string{"path": "src/"},
	}
	res, err := Run(context.Background(), *c, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(prompts[0], "diff-body") {
		t.Errorf("llm prompt not resolved: %q", prompts[0])
	}
	if res.Artifacts["analyze.report"] != "通过" {
		t.Errorf("analyze.report = %q", res.Artifacts["analyze.report"])
	}
	if res.Artifacts["report"] != "saved" {
		t.Errorf("report = %q", res.Artifacts["report"])
	}
	if _, ok := res.Artifacts["publish.collect"]; !ok {
		t.Errorf("publish.collect missing, artifacts=%v", res.Artifacts)
	}
}

func TestExampleChainsValidate(t *testing.T) {
	known := map[string]bool{
		"git_diff": true, "write_file": true, "git_log": true,
		"run_tests": true, "read_file": true,
	}
	for name, c := range ExampleChains() {
		if errs := Validate(c, ValidateOptions{KnownTools: known}); len(errs) > 0 {
			t.Errorf("example chain %q fails validation: %v", name, errs)
		}
	}
}

// ---------------------------------------------------------------------------
// 附加：ctx 取消
// ---------------------------------------------------------------------------

func TestContextCancellation(t *testing.T) {
	chain := Toolchain{
		Name: "cc",
		Steps: []Step{
			{ID: "a", Type: StepTypeTool, Tool: "slow"},
			{ID: "b", Type: StepTypeTool, Tool: "after", DependsOn: Depends{"a"}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	opts := RunOptions{
		Tools: func(name string, params map[string]any) (string, error) {
			if name == "slow" {
				// 运行中取消。
				go func() { time.Sleep(20 * time.Millisecond); cancel() }()
				time.Sleep(200 * time.Millisecond)
				return "slow-done", nil
			}
			return "after", nil
		},
	}
	res, err := Run(ctx, chain, opts)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "b" {
		t.Errorf("Skipped = %v, want [b]", res.Skipped)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func contains(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}
