package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- ⑦ Employee / TeamPlan YAML round-trip ---

func TestEmployeeYAMLRoundTrip(t *testing.T) {
	emp := Employee{
		Name:          "coder",
		Display:       "开发者小倔",
		Role:          "实现代码与修复缺陷",
		Persona:       "严谨、先看测试再动手",
		Model:         ModelBinding{Preset: "flash-model"},
		ToolAllowlist: []string{"read_file", "write_file", "run_tests"},
		ToolDenylist:  []string{"handoff"},
		Memory:        MemorySlice{Slice: "project:code"},
		Approval:      ApprovalStrict,
		Skills:        []string{"refactor", "code-review"},
	}
	data, err := emp.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(*got, emp) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", *got, emp)
	}
}

func TestTeamYAMLRoundTrip(t *testing.T) {
	plan := TeamPlan{
		Name: "pr-pipeline",
		Nodes: []NodeSpec{
			{ID: "review", Role: "reviewer", Toolchain: "code-review-chain", DependsOn: []string{}},
			{ID: "fix", Role: "coder", Toolchain: "fix-chain", DependsOn: []string{"review"}},
			{ID: "test", Role: "tester", Toolchain: "test-chain", DependsOn: []string{"fix"}},
		},
		Output: OutputSpec{From: "test"},
		Policy: PolicySpec{MaxFanout: 4, Retry: 2},
	}
	data, err := plan.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := ParseTeam(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(*got, plan) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", *got, plan)
	}
}

func TestLoadFromDirectory(t *testing.T) {
	dir := t.TempDir()
	emp := ExampleEmployees()[1]
	data, _ := emp.Marshal()
	if err := os.WriteFile(filepath.Join(dir, EmployeeFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Name != "coder" {
		t.Fatalf("loaded name = %q, want coder", got.Name)
	}
}

// --- ⑥ EmployeePolicy: preset / approval / allow-deny merge ---

func TestEmployeePolicyApprovalDefault(t *testing.T) {
	e := Employee{Name: "x"} // empty approval → standard
	p, err := e.ResolvePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if p.Approval != ApprovalStandard {
		t.Fatalf("approval = %q, want standard", p.Approval)
	}
	if p.ModelPreset != "" {
		t.Fatalf("preset = %q, want empty (gateway-routed)", p.ModelPreset)
	}
}

func TestEmployeePolicyDenylistWins(t *testing.T) {
	e := Employee{
		ToolAllowlist: []string{"a", "b", "c", "a"},
		ToolDenylist:  []string{"b"},
	}
	p, err := e.ResolvePolicy()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "c"} // b denied, duplicate a collapsed
	if !reflect.DeepEqual(p.Tools, want) {
		t.Fatalf("tools = %v, want %v", p.Tools, want)
	}
}

func TestEmployeePolicyEmptyAllowlist(t *testing.T) {
	e := Employee{ToolDenylist: []string{"x"}}
	p, _ := e.ResolvePolicy()
	if p.Tools != nil {
		t.Fatalf("empty allowlist should yield nil tools, got %v", p.Tools)
	}
}

func TestEmployeePolicyInvalidApproval(t *testing.T) {
	e := Employee{Name: "x", Approval: "auto"}
	if _, err := e.ResolvePolicy(); err == nil {
		t.Fatal("expected error for invalid approval, got nil")
	}
}

// --- ③ cycle detection with readable path ---

func TestBuildGraphCycleDetected(t *testing.T) {
	plan := TeamPlan{
		Name: "t",
		Nodes: []NodeSpec{
			{ID: "a", DependsOn: []string{"b"}},
			{ID: "b", DependsOn: []string{"c"}},
			{ID: "c", DependsOn: []string{"a"}},
		},
	}
	_, err := BuildGraph(plan)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "cycle") {
		t.Fatalf("error should mention cycle: %s", msg)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(msg, id) {
			t.Fatalf("cycle path should include %q: %s", id, msg)
		}
	}
}

// --- ④ missing dependency ---

func TestBuildGraphMissingDependency(t *testing.T) {
	plan := TeamPlan{
		Name: "t",
		Nodes: []NodeSpec{
			{ID: "a", DependsOn: []string{"ghost"}},
		},
	}
	_, err := BuildGraph(plan)
	if err == nil {
		t.Fatal("expected missing-dependency error, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should name missing node 'ghost': %v", err)
	}
}

func TestBuildGraphDuplicateNode(t *testing.T) {
	plan := TeamPlan{Name: "t", Nodes: []NodeSpec{{ID: "a"}, {ID: "a"}}}
	if _, err := BuildGraph(plan); err == nil {
		t.Fatal("expected duplicate-node error")
	}
}

func TestBuildGraphMissingOutput(t *testing.T) {
	plan := TeamPlan{Name: "t", Nodes: []NodeSpec{{ID: "a"}}, Output: OutputSpec{From: "zzz"}}
	if _, err := BuildGraph(plan); err == nil {
		t.Fatal("expected missing output.from error")
	}
}

// --- ① strict dependency order ---

func TestRunRespectsDependencyOrder(t *testing.T) {
	plan := TeamPlan{
		Name: "chain",
		Nodes: []NodeSpec{
			{ID: "d", DependsOn: []string{"c"}},
			{ID: "c", DependsOn: []string{"b"}},
			{ID: "b", DependsOn: []string{"a"}},
			{ID: "a"},
		},
		Policy: PolicySpec{MaxFanout: 4},
	}
	var mu sync.Mutex
	var order []string
	done := map[string]bool{}
	exec := func(_ context.Context, node NodeSpec) (string, error) {
		mu.Lock()
		done[node.ID] = true
		order = append(order, node.ID)
		mu.Unlock()
		return node.ID, nil
	}
	_, err := Run(context.Background(), plan, exec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Every node must only start after all its dependencies are done.
	g, _ := BuildGraph(plan)
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for id, deps := range g.dependsOn {
		for _, dep := range deps {
			if pos[dep] >= pos[id] {
				t.Fatalf("dependency violated: %q started before its dep %q (order %v)", id, dep, order)
			}
		}
	}
}

// --- ② parallelism + max_fanout constraint ---

func TestRunMaxFanoutLimitsConcurrency(t *testing.T) {
	plan := TeamPlan{
		Name: "fan",
		Nodes: []NodeSpec{
			{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"},
		},
		Policy: PolicySpec{MaxFanout: 2},
	}
	var mu sync.Mutex
	var cur, peak int
	exec := func(_ context.Context, node NodeSpec) (string, error) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
		return node.ID, nil
	}
	if _, err := Run(context.Background(), plan, exec); err != nil {
		t.Fatalf("run: %v", err)
	}
	if peak > 2 {
		t.Fatalf("max concurrent = %d, want <= 2", peak)
	}
	if peak < 2 {
		t.Fatalf("expected at least one wave of 2 parallel nodes, peak = %d", peak)
	}
}

func TestRunUnlimitedFanoutRunsAllInParallel(t *testing.T) {
	plan := TeamPlan{
		Name:   "parallel",
		Nodes:  []NodeSpec{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Policy: PolicySpec{MaxFanout: 0}, // unlimited
	}
	var mu sync.Mutex
	var cur, peak int
	exec := func(_ context.Context, node NodeSpec) (string, error) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		cur--
		mu.Unlock()
		return node.ID, nil
	}
	if _, err := Run(context.Background(), plan, exec); err != nil {
		t.Fatalf("run: %v", err)
	}
	if peak != 3 {
		t.Fatalf("expected all 3 nodes in parallel, peak = %d", peak)
	}
}

// --- ⑤ retry effectiveness ---

func TestRunRetrySucceedsEventually(t *testing.T) {
	plan := TeamPlan{
		Name:   "retry",
		Nodes:  []NodeSpec{{ID: "a"}},
		Output: OutputSpec{From: "a"},
		Policy: PolicySpec{Retry: 2},
	}
	attempts := 0
	exec := func(_ context.Context, _ NodeSpec) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("transient")
		}
		return "ok", nil
	}
	res, err := Run(context.Background(), plan, exec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if res.Output != "ok" {
		t.Fatalf("output = %q, want ok", res.Output)
	}
}

func TestRunRetryExhaustedReportsNodeAndRole(t *testing.T) {
	plan := TeamPlan{
		Name:   "retry-all",
		Nodes:  []NodeSpec{{ID: "bad", Role: "coder"}},
		Policy: PolicySpec{Retry: 1},
	}
	exec := func(_ context.Context, _ NodeSpec) (string, error) {
		return "", errors.New("boom")
	}
	res, err := Run(context.Background(), plan, exec)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	msg := err.Error()
	for _, want := range []string{"bad", "coder", "2 attempt"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error should contain %q: %s", want, msg)
		}
	}
}

func TestRunFailureBlocksDependents(t *testing.T) {
	ran := map[string]bool{}
	var mu sync.Mutex
	plan := TeamPlan{
		Name: "blocked",
		Nodes: []NodeSpec{
			{ID: "good"},
			{ID: "bad"},
			{ID: "after", DependsOn: []string{"bad"}},
			{ID: "after-good", DependsOn: []string{"good"}},
		},
		Policy: PolicySpec{MaxFanout: 4},
	}
	exec := func(_ context.Context, node NodeSpec) (string, error) {
		mu.Lock()
		ran[node.ID] = true
		mu.Unlock()
		if node.ID == "bad" {
			return "", errors.New("fail")
		}
		return node.ID, nil
	}
	_, err := Run(context.Background(), plan, exec)
	if err == nil {
		t.Fatal("expected error")
	}
	mu.Lock()
	defer mu.Unlock()
	if ran["after"] {
		t.Fatal("node depending on failed 'bad' should not run")
	}
	if !ran["after-good"] {
		t.Fatal("independent node 'after-good' should have run")
	}
}

// --- ⑥ output.from collection ---

func TestRunCollectsOutput(t *testing.T) {
	plan := TeamPlan{
		Name: "pipeline",
		Nodes: []NodeSpec{
			{ID: "a"},
			{ID: "b", DependsOn: []string{"a"}},
		},
		Output: OutputSpec{From: "b"},
	}
	exec := func(_ context.Context, node NodeSpec) (string, error) {
		return "out-" + node.ID, nil
	}
	res, err := Run(context.Background(), plan, exec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Output != "out-b" {
		t.Fatalf("output = %q, want out-b", res.Output)
	}
	if res.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", res.Status)
	}
	if got := res.NodeOutputs["a"]; got != "out-a" {
		t.Fatalf("NodeOutputs[a] = %q, want out-a", got)
	}
}

// --- ⑧ built-in employees ---

func TestExampleEmployees(t *testing.T) {
	emps := ExampleEmployees()
	if len(emps) != 6 {
		t.Fatalf("expected 6 built-in employees, got %d", len(emps))
	}
	names := map[string]bool{}
	for _, e := range emps {
		names[e.Name] = true
		if e.Name == "" || e.Display == "" || e.Role == "" || e.Persona == "" {
			t.Fatalf("employee %+v missing required fields", e)
		}
		if _, err := NormalizeApproval(e.Approval); err != nil {
			t.Fatalf("employee %q invalid approval: %v", e.Name, err)
		}
	}
	for _, want := range []string{"writer", "coder", "tester", "reviewer", "product", "translator"} {
		if !names[want] {
			t.Fatalf("missing built-in employee %q (got %v)", want, sortedKeys(names))
		}
	}
}

func TestExampleEmployeesHaveDistinctToolkits(t *testing.T) {
	emps := ExampleEmployees()
	kits := map[string]int{}
	for _, e := range emps {
		kits[strings.Join(e.ToolAllowlist, ",")]++
	}
	if len(kits) < 3 {
		t.Fatalf("expected varied toolkits across employees, got %d distinct", len(kits))
	}
	// reviewer must deny writes, coder must allow them.
	var reviewer, coder *Employee
	for i := range emps {
		switch emps[i].Name {
		case "reviewer":
			reviewer = &emps[i]
		case "coder":
			coder = &emps[i]
		}
	}
	if reviewer == nil || coder == nil {
		t.Fatal("missing reviewer/coder")
	}
	if !contains(reviewer.ToolDenylist, "write_file") {
		t.Fatal("reviewer should deny write_file")
	}
	if !contains(coder.ToolAllowlist, "write_file") {
		t.Fatal("coder should allow write_file")
	}
}

func TestExampleEmployeesMarshalParseAll(t *testing.T) {
	for _, e := range ExampleEmployees() {
		data, err := e.Marshal()
		if err != nil {
			t.Fatalf("marshal %q: %v", e.Name, err)
		}
		if _, err := Parse(data); err != nil {
			t.Fatalf("parse %q: %v", e.Name, err)
		}
	}
}

// --- helpers ---

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- boundary cases: context cancellation, empty team, negative retry ---

func TestRunContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plan := TeamPlan{
		Name:   "cancel",
		Nodes:  []NodeSpec{{ID: "a"}},
		Output: OutputSpec{From: "a"},
	}
	var once sync.Once
	exec := func(ctx context.Context, _ NodeSpec) (string, error) {
		once.Do(cancel) // cancel while the node is already running
		<-ctx.Done()
		return "", ctx.Err()
	}
	res, err := Run(ctx, plan, exec)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
}

func TestRunCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan := TeamPlan{Name: "cancel", Nodes: []NodeSpec{{ID: "a"}}}
	started := false
	exec := func(_ context.Context, _ NodeSpec) (string, error) {
		started = true
		return "x", nil
	}
	if _, err := Run(ctx, plan, exec); err == nil {
		t.Fatal("expected cancellation error")
	}
	if started {
		t.Fatal("no node should start after ctx is already cancelled")
	}
}

func TestRunEmptyTeam(t *testing.T) {
	res, err := Run(context.Background(), TeamPlan{Name: "empty"}, func(context.Context, NodeSpec) (string, error) {
		return "x", nil
	})
	if err != nil {
		t.Fatalf("empty team should succeed: %v", err)
	}
	if res.Status != StatusCompleted || len(res.NodeOutputs) != 0 {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestRunNegativeRetryClampedToZero(t *testing.T) {
	plan := TeamPlan{
		Name:   "clamp",
		Nodes:  []NodeSpec{{ID: "a"}},
		Policy: PolicySpec{Retry: -5},
	}
	attempts := 0
	exec := func(_ context.Context, _ NodeSpec) (string, error) {
		attempts++
		return "", errors.New("boom")
	}
	_, err := Run(context.Background(), plan, exec)
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (retry clamped to 0)", attempts)
	}
}
