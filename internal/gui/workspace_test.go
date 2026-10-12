package gui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newWSServer 构造一个带独立项目根的 GUI 测试服务（fakeRuntime.ConfigSnapshot
// 不返回四目录键 → 走 ProjectRoot()/.contextloom/<品类> fallback，见 TestObjectDirs_Fallback）。
func newWSServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	s := NewServer(&fakeRuntime{projectRoot: root}, 0, "test-token")
	return s, root
}

// assertPath 断言结果为 {path: ...} 且落盘在期望路径。
func assertPath(t *testing.T, method string, res any, want string) {
	t.Helper()
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("%s 结果应为对象, 得到 %T", method, res)
	}
	p, ok := m["path"].(string)
	if !ok || p == "" {
		t.Fatalf("%s 缺 path: %v", method, m)
	}
	wantAbs := want
	if abs, err := filepath.Abs(want); err == nil {
		wantAbs = abs
	}
	if p != wantAbs && p != want {
		t.Fatalf("%s path = %q, 期望 %q", method, p, want)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s 落盘文件不可读: %v", method, err)
	}
}

func TestObjectDirs_Fallback(t *testing.T) {
	// fakeRuntime.ConfigSnapshot 不含四目录键 → 必须回退 ProjectRoot()/.contextloom/<品类>
	s, root := newWSServer(t)
	dirs := s.objectDirs()
	for _, kind := range []string{"employees", "scenes", "chains", "gates"} {
		want := filepath.Join(root, ".contextloom", kind)
		if dirs[kind] != want {
			t.Errorf("objectDirs[%q] = %q, 期望 %q (fallback)", kind, dirs[kind], want)
		}
	}
}

func TestEmployeeCRUD_RoundTrip(t *testing.T) {
	s, root := newWSServer(t)
	ctx := context.Background()

	yaml1 := "name: boss\ndisplay: 老板\nrole: 拍板与决策\n"
	// 1. save（新对象，force=false）
	res, err := s.dispatch(ctx, "employee.save", mustRaw(t, map[string]any{
		"name": "boss", "yaml": yaml1,
	}), nil)
	if err != nil {
		t.Fatalf("employee.save: %v", err)
	}
	assertPath(t, "employee.save", res, filepath.Join(root, ".contextloom", "employees", "boss", "employee.yaml"))

	// 2. list 含 1 项
	res, err = s.dispatch(ctx, "employee.list", mustRaw(t, map[string]any{}), nil)
	if err != nil {
		t.Fatalf("employee.list: %v", err)
	}
	lm := res.(map[string]any)
	items := lm["items"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("list 期望 1 项, 得到 %d (%v)", len(items), items)
	}
	it := items[0]
	if it["name"] != "boss" || it["display"] != "老板" || it["role"] != "拍板与决策" {
		t.Fatalf("list 摘要错误: %v", it)
	}
	if p, _ := it["path"].(string); p == "" {
		t.Fatalf("list 项缺 path")
	}

	// 3. get 返回 yaml 一致
	res, err = s.dispatch(ctx, "employee.get", mustRaw(t, map[string]any{"name": "boss"}), nil)
	if err != nil {
		t.Fatalf("employee.get: %v", err)
	}
	gm := res.(map[string]any)
	if gm["name"] != "boss" || gm["yaml"] != yaml1 {
		t.Fatalf("get 返回不一致: name=%v yaml=%q 期望 %q", gm["name"], gm["yaml"], yaml1)
	}

	// 4. 同名不 force 报错
	if _, err = s.dispatch(ctx, "employee.save", mustRaw(t, map[string]any{
		"name": "boss", "yaml": "name: boss\nrole: 新角色\n",
	}), nil); err == nil || !contains(err.Error(), "已存在") {
		t.Fatalf("save 同名不 force 应报'已存在', 得到 %v", err)
	}

	// 5. force 覆盖成功
	yaml2 := "name: boss\ndisplay: 老板2\nrole: 决策2\n"
	res, err = s.dispatch(ctx, "employee.save", mustRaw(t, map[string]any{
		"name": "boss", "yaml": yaml2, "force": true,
	}), nil)
	if err != nil {
		t.Fatalf("employee.save force: %v", err)
	}
	res, _ = s.dispatch(ctx, "employee.get", mustRaw(t, map[string]any{"name": "boss"}), nil)
	if res.(map[string]any)["yaml"] != yaml2 {
		t.Fatalf("force 覆盖后 get = %q, 期望 %q", res.(map[string]any)["yaml"], yaml2)
	}

	// 6. delete 后 list 为空
	res, err = s.dispatch(ctx, "employee.delete", mustRaw(t, map[string]any{"name": "boss"}), nil)
	if err != nil {
		t.Fatalf("employee.delete: %v", err)
	}
	if res.(map[string]any)["deleted"] != true {
		t.Fatalf("delete 应返回 deleted:true, 得到 %v", res)
	}
	res, _ = s.dispatch(ctx, "employee.list", mustRaw(t, map[string]any{}), nil)
	items = res.(map[string]any)["items"].([]map[string]any)
	if len(items) != 0 {
		t.Fatalf("delete 后 list 应空, 得到 %v", items)
	}

	// 7. get 已删除对象报错
	if _, err = s.dispatch(ctx, "employee.get", mustRaw(t, map[string]any{"name": "boss"}), nil); err == nil {
		t.Fatal("get 已删除对象应报错")
	}
}

func TestEmployeeSave_BadName(t *testing.T) {
	s, _ := newWSServer(t)
	ctx := context.Background()
	cases := []string{"../evil", "a/b", "a\\b", "", "  "}
	for _, name := range cases {
		_, err := s.dispatch(ctx, "employee.save", mustRaw(t, map[string]any{
			"name": name, "yaml": "name: x\n",
		}), nil)
		if err == nil {
			t.Errorf("name=%q 应被拒绝", name)
		}
	}
}

func TestEmployeeSave_BadYAML(t *testing.T) {
	s, _ := newWSServer(t)
	ctx := context.Background()
	for _, y := range []string{"", "   ", "a: [unclosed", "- list\n- item\n", "just a scalar"} {
		_, err := s.dispatch(ctx, "employee.save", mustRaw(t, map[string]any{
			"name": "ok", "yaml": y,
		}), nil)
		if err == nil {
			t.Errorf("yaml=%q 应被拒绝", y)
		}
	}
}

func TestEmployeeSave_NameMismatch(t *testing.T) {
	s, _ := newWSServer(t)
	// YAML 内 name 与传入 name 不一致必须拒绝写盘
	_, err := s.dispatch(context.Background(), "employee.save", mustRaw(t, map[string]any{
		"name": "a", "yaml": "name: b\nrole: x\n",
	}), nil)
	if err == nil || !contains(err.Error(), "不一致") {
		t.Fatalf("name 不一致应报错, 得到 %v", err)
	}
}

func TestSceneToolchainCRUD(t *testing.T) {
	s, root := newWSServer(t)
	ctx := context.Background()

	// ── 场景 ──
	sceneYAML := "name: weekly\ndisplay: 周报助手\nkeywords:\n  - 周报\n  - 周总结\n"
	res, err := s.dispatch(ctx, "scene.save", mustRaw(t, map[string]any{
		"name": "weekly", "yaml": sceneYAML,
	}), nil)
	if err != nil {
		t.Fatalf("scene.save: %v", err)
	}
	assertPath(t, "scene.save", res, filepath.Join(root, ".contextloom", "scenes", "weekly", "scene.yaml"))

	res, _ = s.dispatch(ctx, "scene.get", mustRaw(t, map[string]any{"name": "weekly"}), nil)
	gm := res.(map[string]any)
	if gm["yaml"] != sceneYAML {
		t.Fatalf("scene.get yaml = %q", gm["yaml"])
	}

	res, _ = s.dispatch(ctx, "scene.list", mustRaw(t, map[string]any{}), nil)
	items := res.(map[string]any)["items"].([]map[string]any)
	if len(items) != 1 || items[0]["name"] != "weekly" || items[0]["display"] != "周报助手" {
		t.Fatalf("scene.list 错误: %v", items)
	}
	if kw, ok := items[0]["keywords"].([]any); !ok || !reflect.DeepEqual(kw, []any{"周报", "周总结"}) {
		t.Fatalf("scene.list keywords 错误: %v (%T)", items[0]["keywords"], items[0]["keywords"])
	}

	// ── 工具链 ──
	chainYAML := "name: review\nversion: 1.0.0\nsteps:\n  - id: a\n    type: tool\n    tool: git_diff\n"
	res, err = s.dispatch(ctx, "toolchain.save", mustRaw(t, map[string]any{
		"name": "review", "yaml": chainYAML,
	}), nil)
	if err != nil {
		t.Fatalf("toolchain.save: %v", err)
	}
	assertPath(t, "toolchain.save", res, filepath.Join(root, ".contextloom", "chains", "review", "toolchain.yaml"))

	res, _ = s.dispatch(ctx, "toolchain.get", mustRaw(t, map[string]any{"name": "review"}), nil)
	if res.(map[string]any)["yaml"] != chainYAML {
		t.Fatalf("toolchain.get yaml 不一致: %q", res.(map[string]any)["yaml"])
	}

	res, _ = s.dispatch(ctx, "toolchain.list", mustRaw(t, map[string]any{}), nil)
	items = res.(map[string]any)["items"].([]map[string]any)
	if len(items) != 1 || items[0]["name"] != "review" || items[0]["version"] != "1.0.0" || items[0]["steps"] != 1 {
		t.Fatalf("toolchain.list 错误: %v", items)
	}

	// ── 删除场景与工具链 ──
	if _, err = s.dispatch(ctx, "scene.delete", mustRaw(t, map[string]any{"name": "weekly"}), nil); err != nil {
		t.Fatalf("scene.delete: %v", err)
	}
	if _, err = s.dispatch(ctx, "toolchain.delete", mustRaw(t, map[string]any{"name": "review"}), nil); err != nil {
		t.Fatalf("toolchain.delete: %v", err)
	}
	res, _ = s.dispatch(ctx, "scene.list", mustRaw(t, map[string]any{}), nil)
	if n := len(res.(map[string]any)["items"].([]map[string]any)); n != 0 {
		t.Fatalf("scene.delete 后 list 应空, 得到 %d", n)
	}
	res, _ = s.dispatch(ctx, "toolchain.list", mustRaw(t, map[string]any{}), nil)
	if n := len(res.(map[string]any)["items"].([]map[string]any)); n != 0 {
		t.Fatalf("toolchain.delete 后 list 应空, 得到 %d", n)
	}
}

func TestGateList_DefaultAndSave(t *testing.T) {
	s, root := newWSServer(t)
	ctx := context.Background()

	// 1. gates 目录不存在 → enabled:false
	res, err := s.dispatch(ctx, "gate.list", mustRaw(t, map[string]any{}), nil)
	if err != nil {
		t.Fatalf("gate.list: %v", err)
	}
	if res.(map[string]any)["enabled"] != false {
		t.Fatalf("空项目 gate.list 应 enabled:false, 得到 %v", res)
	}

	// 2. gate.save 创目录 + 写 gate.yaml
	res, err = s.dispatch(ctx, "gate.save", mustRaw(t, map[string]any{
		"level": "standard", "pass_score": 90,
	}), nil)
	if err != nil {
		t.Fatalf("gate.save: %v", err)
	}
	assertPath(t, "gate.save", res, filepath.Join(root, ".contextloom", "gates", "gate.yaml"))

	// 3. list 返回 level/pass_score
	res, err = s.dispatch(ctx, "gate.list", mustRaw(t, map[string]any{}), nil)
	if err != nil {
		t.Fatalf("gate.list: %v", err)
	}
	gm := res.(map[string]any)
	if gm["enabled"] != true || gm["level"] != "standard" || gm["pass_score"] != 90 {
		t.Fatalf("gate.list = %v, 期望 enabled/standard/90", gm)
	}
	if _, hasLedger := gm["ledger_yaml"]; hasLedger {
		t.Fatalf("无账本时不应出现 ledger_yaml 键: %v", gm)
	}

	// 4. 非法 level / pass_score 越界报错
	if _, err = s.dispatch(ctx, "gate.save", mustRaw(t, map[string]any{
		"level": "turbo", "pass_score": 90,
	}), nil); err == nil {
		t.Fatal("非法 level 应报错")
	}
	if _, err = s.dispatch(ctx, "gate.save", mustRaw(t, map[string]any{
		"level": "fast", "pass_score": 101,
	}), nil); err == nil {
		t.Fatal("pass_score=101 应报错")
	}
	if _, err = s.dispatch(ctx, "gate.save", mustRaw(t, map[string]any{
		"level": "none", "pass_score": -1,
	}), nil); err == nil {
		t.Fatal("pass_score=-1 应报错")
	}

	// 5. 合法边界：pass_score=0 与 fast 档均可保存
	if _, err = s.dispatch(ctx, "gate.save", mustRaw(t, map[string]any{
		"level": "fast", "pass_score": 60,
	}), nil); err != nil {
		t.Fatalf("fast/60 应可保存: %v", err)
	}
	res, _ = s.dispatch(ctx, "gate.list", mustRaw(t, map[string]any{}), nil)
	gm = res.(map[string]any)
	if gm["level"] != "fast" || gm["pass_score"] != 60 {
		t.Fatalf("fast/60 保存后 list = %v", gm)
	}
}

func TestGateLedger_ReadOnly(t *testing.T) {
	s, root := newWSServer(t)
	// 手工放置 gates 目录 + ledger（模拟 runtime 落盘）
	gatesDir := filepath.Join(root, ".contextloom", "gates")
	if err := os.MkdirAll(gatesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := "defect_history:\n  - signature: 改完不跑测试\n    count: 3\n"
	if err := os.WriteFile(filepath.Join(gatesDir, "quality_ledger.yaml"), []byte(ledger), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := s.dispatch(context.Background(), "gate.list", mustRaw(t, map[string]any{}), nil)
	if err != nil {
		t.Fatalf("gate.list: %v", err)
	}
	gm := res.(map[string]any)
	if gm["ledger_yaml"] != ledger {
		t.Fatalf("ledger_yaml = %q, 期望 %q", gm["ledger_yaml"], ledger)
	}
	// 未写 gate.yaml → 默认 standard/90
	if gm["level"] != "standard" || gm["pass_score"] != 90 {
		t.Fatalf("无 gate.yaml 应默认 standard/90, 得到 %v", gm)
	}
}

// mustRaw 序列化参数（测试辅助）。
func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("参数序列化失败: %v", err)
	}
	return b
}

// contains 子串断言辅助。
func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
