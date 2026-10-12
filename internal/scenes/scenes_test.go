package scenes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// 测试辅助
// ---------------------------------------------------------------------------

const weekSceneYAML = `name: office-weekly
display: "周报"
keywords: [周报, weekly]
intent_prompt: 判断用户是否要写周报
team: report-team
models: { policy: "budget=low" }
toolchain: weekly-report-chain
approval: standard
output: { path: "docs/weekly/{{date}}.md", autoload: true }
verify: overseer
triggers: { cron: "0 17 * * 5" }
`

const prSceneYAML = `name: dev-pr-review
display: "PR 审查"
keywords: [review, 审查]
intent_prompt: 判断用户是否需要 PR 审查报告
team: review-team
models: { policy: "role=coder" }
toolchain: code-review-chain
approval: standard
output: { path: "docs/review/{{date}}.md", autoload: false }
verify: overseer
`

// writeScene 在 dir/name/scene.yaml 写入场景 YAML，返回子目录路径。
func writeScene(t *testing.T, dir, name, content string) string {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("创建场景目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "scene.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入场景文件失败: %v", err)
	}
	return sub
}

func mustParse(t *testing.T, data string) *Scene {
	t.Helper()
	s, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	return s
}

func fixedNow() time.Time {
	return time.Date(2026, 8, 30, 9, 5, 7, 0, time.UTC)
}

// ---------------------------------------------------------------------------
// ① keywords 规则层命中（大小写不敏感）
// ---------------------------------------------------------------------------

func TestMatchRuleLayerKeywords(t *testing.T) {
	cat := DefaultBuiltinScenes()

	cases := []struct {
		input string
		want  string
	}{
		{"帮我写周报", "weekly-report"},                // 中文关键词
		{"Weekly Report please", "weekly-report"}, // 英文关键词大小写不敏感
		{"review 一下这段代码", "pr-review"},
		{"帮我出一份PRD", "prd"},
		{"写个公众号推文", "post"},
	}
	for _, c := range cases {
		got := Match(cat, c.input, nil)
		if got == nil {
			t.Errorf("Match(%q) = nil, 期望命中 %q", c.input, c.want)
			continue
		}
		if got.Name != c.want {
			t.Errorf("Match(%q) = %q, 期望 %q", c.input, got.Name, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ② 规则未命中 → 模型层 classifier 兜底命中
// ---------------------------------------------------------------------------

func TestMatchModelLayerFallback(t *testing.T) {
	cat := DefaultBuiltinScenes()
	// mock 分类器：只有 prd 场景的 intent_prompt 被问到时返回命中
	classifier := func(intentPrompt, userInput string) (bool, error) {
		if strings.Contains(intentPrompt, "PRD") && strings.Contains(userInput, "需求") {
			return true, nil
		}
		return false, nil
	}

	got := Match(cat, "帮我梳理一下这个需求", classifier)
	if got == nil {
		t.Fatal("模型层应兜底命中 prd 场景，实际返回 nil")
	}
	if got.Name != "prd" {
		t.Fatalf("模型层兜底命中 = %q, 期望 prd", got.Name)
	}
}

// 模型层单个场景判断出错 → 跳过该场景继续尝试，不向上抛错（绝不阻塞）。
func TestMatchModelLayerErrorSkipped(t *testing.T) {
	cat := DefaultBuiltinScenes()
	classifier := func(intentPrompt, userInput string) (bool, error) {
		if strings.Contains(intentPrompt, "PRD") {
			return false, errors.New("模型暂时不可用")
		}
		if strings.Contains(intentPrompt, "周报") && strings.Contains(userInput, "总结") {
			return true, nil
		}
		return false, nil
	}

	got := Match(cat, "写个总结", classifier)
	if got == nil || got.Name != "weekly-report" {
		t.Fatalf("分类器出错场景应被跳过并继续匹配，got=%v", got)
	}
}

// ---------------------------------------------------------------------------
// ③ 全部未命中返回 nil（零感知兜底）
// ---------------------------------------------------------------------------

func TestMatchMissReturnsNil(t *testing.T) {
	cat := DefaultBuiltinScenes()

	if got := Match(cat, "今天天气怎么样", nil); got != nil {
		t.Fatalf("无关键词命中应返回 nil, got %q", got.Name)
	}
	// 空输入也不应 panic 或命中
	if got := Match(cat, "", nil); got != nil {
		t.Fatalf("空输入应返回 nil, got %q", got.Name)
	}
	// classifier 全部判否 → nil
	classifier := func(_, _ string) (bool, error) { return false, nil }
	if got := Match(cat, "随便聊聊", classifier); got != nil {
		t.Fatalf("分类器全判否应返回 nil, got %q", got.Name)
	}
}

// ---------------------------------------------------------------------------
// ④ YAML 往返（Parse → Marshal → Parse 等价）
// ---------------------------------------------------------------------------

func TestYAMLRoundTrip(t *testing.T) {
	orig := mustParse(t, weekSceneYAML)

	data, err := orig.Marshal()
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}

	got, err := Parse(data)
	if err != nil {
		t.Fatalf("往返后 Parse 失败: %v", err)
	}

	if !reflect.DeepEqual(got, orig) {
		t.Fatalf("YAML 往返不一致:\n原始: %+v\n往返: %+v", orig, got)
	}

	// 内置四场景逐个往返（覆盖全部字段）
	for _, s := range DefaultBuiltinScenes() {
		b, err := s.Marshal()
		if err != nil {
			t.Fatalf("场景 %q Marshal 失败: %v", s.Name, err)
		}
		back, err := Parse(b)
		if err != nil {
			t.Fatalf("场景 %q 往返 Parse 失败: %v", s.Name, err)
		}
		if !reflect.DeepEqual(back, &s) {
			t.Fatalf("场景 %q 往返不一致:\n%+v\n%+v", s.Name, back, &s)
		}
	}
}

// Parse 缺少 name 报错
func TestParseMissingName(t *testing.T) {
	if _, err := Parse([]byte("display: 缺名场景\n")); err == nil {
		t.Fatal("缺少 name 的 YAML 应报错")
	}
	// 非法 YAML 报错
	if _, err := Parse([]byte(":\n\tbad")); err == nil {
		t.Fatal("非法 YAML 应报错")
	}
	// yaml 原始库往返一致性抽查
	b, err := yaml.Marshal(mustParse(t, prSceneYAML))
	if err != nil {
		t.Fatalf("yaml.Marshal 失败: %v", err)
	}
	if _, err := Parse(b); err != nil {
		t.Fatalf("yaml.Marshal 产物 Parse 失败: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ⑤ {{date}} 模板渲染与 output.path 渲染
// ---------------------------------------------------------------------------

func TestInstantiateTemplates(t *testing.T) {
	s := mustParse(t, weekSceneYAML)
	inst, err := Instantiate(*s, "帮我写周报", fixedNow())
	if err != nil {
		t.Fatalf("Instantiate 失败: %v", err)
	}

	if got := inst.Params["date"]; got != "2026-08-30" {
		t.Errorf("Params[date] = %q, 期望 2026-08-30", got)
	}
	if got := inst.Params["time"]; got != "09:05:07" {
		t.Errorf("Params[time] = %q, 期望 09:05:07", got)
	}
	if got := inst.Params["user_input"]; got != "帮我写周报" {
		t.Errorf("Params[user_input] = %q", got)
	}
	// output.path 模板渲染
	if got := inst.OutputPath; got != "docs/weekly/2026-08-30.md" {
		t.Errorf("OutputPath = %q, 期望 docs/weekly/2026-08-30.md", got)
	}
}

// RenderTemplate 直接渲染（含多变量与未知变量保留）
func TestRenderTemplate(t *testing.T) {
	params := map[string]string{"date": "2026-08-30", "time": "09:05:07", "user_input": "写周报"}
	got := RenderTemplate("{{date}}/{{time}}-{{user_input}}-{{unknown}}", params)
	want := "2026-08-30/09:05:07-写周报-{{unknown}}"
	if got != want {
		t.Fatalf("RenderTemplate = %q, 期望 %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// ⑥ 路径逃逸校验（../ 拒绝）
// ---------------------------------------------------------------------------

func TestValidateOutputPathRejectsEscape(t *testing.T) {
	bad := []string{
		"../etc/passwd",
		"docs/../../etc/passwd",
		"..",
		"../",
		"..\\evil.txt",        // 反斜杠逃逸（跨平台统一拒绝）
		"/abs/path.md",        // Unix 绝对路径
		"C:\\Windows\\x.md",   // Windows 盘符绝对路径
		"docs/..\\..\\secret", // 混合分隔符逃逸
	}
	for _, p := range bad {
		if err := ValidateOutputPath(p); err == nil {
			t.Errorf("ValidateOutputPath(%q) 应拒绝逃逸", p)
		}
	}

	good := []string{
		"",
		"docs/weekly/2026-08-30.md",
		"docs/weekly/sub/dir/report.md",
		"report-2026-08-30.md",
	}
	for _, p := range good {
		if err := ValidateOutputPath(p); err != nil {
			t.Errorf("ValidateOutputPath(%q) 应通过, got %v", p, err)
		}
	}
}

// Instantiate 对渲染后路径逃逸应报可读错误
func TestInstantiateRejectsEscape(t *testing.T) {
	s := mustParse(t, weekSceneYAML)
	s.Output.Path = "docs/{{user_input}}.md"
	_, err := Instantiate(*s, "../../secret", fixedNow())
	if err == nil {
		t.Fatal("user_input 注入 .. 逃逸应报错")
	}
	if !strings.Contains(err.Error(), "逃逸") && !strings.Contains(err.Error(), "非法") {
		t.Fatalf("错误信息应可读: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ⑦ LoadAll 目录扫描
// ---------------------------------------------------------------------------

func TestLoadAllDirScan(t *testing.T) {
	dir := t.TempDir()
	writeScene(t, dir, "office-weekly", weekSceneYAML)
	writeScene(t, dir, "dev-pr-review", prSceneYAML)
	// 干扰项：无 scene.yaml 的子目录、普通文件、空目录
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("无关文件"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	scenes, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll 失败: %v", err)
	}
	if len(scenes) != 2 {
		t.Fatalf("LoadAll 应加载 2 个场景, got %d", len(scenes))
	}
	// 按 name 排序确定性
	if scenes[0].Name != "dev-pr-review" || scenes[1].Name != "office-weekly" {
		t.Fatalf("场景顺序应为按 name 升序, got %q, %q", scenes[0].Name, scenes[1].Name)
	}
	if scenes[0].Display != "PR 审查" {
		t.Fatalf("场景字段解析错误: %+v", scenes[0])
	}
}

// LoadAll 目录不存在报错
func TestLoadAllMissingDir(t *testing.T) {
	if _, err := LoadAll(filepath.Join(t.TempDir(), "not-exist")); err == nil {
		t.Fatal("扫描不存在的目录应报错")
	}
}

// Load 支持目录与文件两种入口
func TestLoadDirAndFile(t *testing.T) {
	dir := t.TempDir()
	sub := writeScene(t, dir, "office-weekly", weekSceneYAML)

	fromDir, err := Load(sub)
	if err != nil {
		t.Fatalf("Load(目录) 失败: %v", err)
	}
	fromFile, err := Load(filepath.Join(sub, "scene.yaml"))
	if err != nil {
		t.Fatalf("Load(文件) 失败: %v", err)
	}
	if fromDir.Name != "office-weekly" || fromFile.Name != "office-weekly" {
		t.Fatalf("Load 两种入口结果应一致, got %q / %q", fromDir.Name, fromFile.Name)
	}
}

// ---------------------------------------------------------------------------
// ⑧ 内置四场景数量与关键词
// ---------------------------------------------------------------------------

func TestDefaultBuiltinScenes(t *testing.T) {
	scenes := DefaultBuiltinScenes()
	if len(scenes) != 4 {
		t.Fatalf("内置场景数 = %d, 期望 4", len(scenes))
	}

	wantNames := map[string]bool{
		"weekly-report": false, "pr-review": false, "prd": false, "post": false,
	}
	keywordOwner := map[string]string{} // 关键词 → 场景，检测跨场景重复
	for _, s := range scenes {
		if _, ok := wantNames[s.Name]; !ok {
			t.Fatalf("存在非预期内置场景 %q", s.Name)
		}
		wantNames[s.Name] = true
		if len(s.Keywords) == 0 {
			t.Errorf("场景 %q 缺少关键词", s.Name)
		}
		for _, kw := range s.Keywords {
			low := strings.ToLower(kw)
			if owner, dup := keywordOwner[low]; dup {
				t.Errorf("关键词 %q 在场景 %q 与 %q 中重复，规则层匹配将歧义", kw, owner, s.Name)
			}
			keywordOwner[low] = s.Name
		}
		if s.IntentPrompt == "" {
			t.Errorf("场景 %q 缺少 intent_prompt", s.Name)
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("内置场景 %q 缺失", name)
		}
	}

	// 抽查关键词内容
	if got := Match(scenes, "帮我写周报", nil); got == nil || got.Name != "weekly-report" {
		t.Errorf("周报关键词应命中 weekly-report, got %v", got)
	}
	if got := Match(scenes, "review my pull request", nil); got == nil || got.Name != "pr-review" {
		t.Errorf("review 关键词应命中 pr-review, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ⑨ Run 成功 / 取消两路
// ---------------------------------------------------------------------------

func TestRunSuccess(t *testing.T) {
	s := mustParse(t, weekSceneYAML)
	inst, err := Instantiate(*s, "写周报", fixedNow())
	if err != nil {
		t.Fatal(err)
	}

	runner := func(ctx context.Context, i Instance) (string, error) {
		if i.Scene.Name != "office-weekly" {
			t.Errorf("Runner 收到的 Instance.Scene.Name = %q", i.Scene.Name)
		}
		return "周报已生成 ✓", nil
	}

	res, err := Run(context.Background(), *inst, runner)
	if err != nil {
		t.Fatalf("Run 应成功: %v", err)
	}
	if res.Status != StatusOK {
		t.Errorf("Status = %q, 期望 %q", res.Status, StatusOK)
	}
	if res.Output != "周报已生成 ✓" {
		t.Errorf("Output = %q", res.Output)
	}
	if res.Scene.Name != "office-weekly" {
		t.Errorf("RunResult.Scene.Name = %q", res.Scene.Name)
	}
	if res.StartedAt.IsZero() {
		t.Error("StartedAt 不应为零值")
	}
	if res.Duration < 0 {
		t.Errorf("Duration = %v, 不应为负", res.Duration)
	}
}

func TestRunCanceled(t *testing.T) {
	s := mustParse(t, weekSceneYAML)
	inst, err := Instantiate(*s, "写周报", fixedNow())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// runner 模拟长任务：仅在 ctx 取消后才返回
	runner := func(ctx context.Context, _ Instance) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	res, err := Run(ctx, *inst, runner)
	if err == nil {
		t.Fatal("取消场景 Run 应返回错误")
	}
	if !strings.Contains(err.Error(), "取消") {
		t.Errorf("取消错误应可读: %v", err)
	}
	if res.Status != StatusCanceled {
		t.Errorf("Status = %q, 期望 %q", res.Status, StatusCanceled)
	}
	if res.Output != "" {
		t.Errorf("取消场景 Output 应为空, got %q", res.Output)
	}
}

func TestRunCanceledUpfront(t *testing.T) {
	s := mustParse(t, weekSceneYAML)
	inst, _ := Instantiate(*s, "写周报", fixedNow())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 执行前已取消

	ran := false
	runner := func(context.Context, Instance) (string, error) {
		ran = true
		return "should not run", nil
	}

	res, err := Run(ctx, *inst, runner)
	if err == nil {
		t.Fatal("已取消的 ctx 应直接报错")
	}
	if res.Status != StatusCanceled {
		t.Errorf("Status = %q, 期望 %q", res.Status, StatusCanceled)
	}
	if ran {
		t.Error("ctx 已取消时 Runner 不应被调用")
	}
}

func TestRunRunnerError(t *testing.T) {
	s := mustParse(t, weekSceneYAML)
	inst, _ := Instantiate(*s, "写周报", fixedNow())

	wantErr := errors.New("工具链执行失败")
	runner := func(context.Context, Instance) (string, error) {
		return "半成品", wantErr
	}

	res, err := Run(context.Background(), *inst, runner)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Runner 错误应透传, got %v", err)
	}
	if res.Status != StatusFailed {
		t.Errorf("Status = %q, 期望 %q", res.Status, StatusFailed)
	}
}

func TestRunNilRunner(t *testing.T) {
	s := mustParse(t, weekSceneYAML)
	inst, _ := Instantiate(*s, "写周报", fixedNow())
	if _, err := Run(context.Background(), *inst, nil); err == nil {
		t.Fatal("nil Runner 应报错")
	}
}
