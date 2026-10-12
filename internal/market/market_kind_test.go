package market

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testMarketWith 构造一个注入自定义条目（含 Kind）的测试市场。
func testMarketWith(items ...Item) *Market {
	m := New()
	m.builtin.Items = append(m.builtin.Items, items...)
	return m
}

func TestItemFileName(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{KindSkill, "SKILL.md"},
		{KindToolchain, "toolchain.yaml"},
		{KindEmployee, "employee.yaml"},
		{KindTeam, "team.yaml"},
		{KindScene, "scene.yaml"},
		{"", "SKILL.md"}, // 空 Kind 向后兼容默认 skill
	}
	for _, tc := range cases {
		it := Item{Name: "x", Kind: tc.kind}
		if got := it.FileName(); got != tc.want {
			t.Errorf("Kind=%q FileName()=%q, want %q", tc.kind, got, tc.want)
		}
		if tc.kind == "" {
			if !it.IsSkill() {
				t.Errorf("Kind=%q IsSkill() 应为 true", tc.kind)
			}
		}
	}
	if !(Item{Kind: KindSkill}).IsSkill() {
		t.Error("Kind=skill IsSkill() 应为 true")
	}
	if (Item{Kind: KindToolchain}).IsSkill() {
		t.Error("Kind=toolchain IsSkill() 应为 false")
	}
	// 未知 Kind：FileName 返回空串（由 Install 校验报错）
	if got := (Item{Kind: "nope"}).FileName(); got != "" {
		t.Errorf("未知 Kind FileName() 应为空串, got %q", got)
	}
}

func TestInstall_EachKind(t *testing.T) {
	cases := []struct {
		kind string
		file string
	}{
		{KindToolchain, "toolchain.yaml"},
		{KindEmployee, "employee.yaml"},
		{KindTeam, "team.yaml"},
		{KindScene, "scene.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			name := "item-" + tc.kind
			m := testMarketWith(Item{
				Name:        name,
				Kind:        tc.kind,
				Description: "测试条目",
				Content:     "name: " + name + "\n---\nbody-" + tc.kind,
			})
			dir := t.TempDir()
			if err := m.Install(name, dir); err != nil {
				t.Fatal(err)
			}
			// 目标入口文件写入且内容正确
			p := filepath.Join(dir, name, tc.file)
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("读取 %s 失败: %v", tc.file, err)
			}
			if !strings.Contains(string(data), "body-"+tc.kind) {
				t.Errorf("%s 内容异常: %s", tc.file, string(data)[:60])
			}
			// 非 skill 品类不应写入 SKILL.md
			if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil {
				t.Error("非 skill 品类不应写 SKILL.md")
			}
			// 重复安装不覆盖
			if err := m.Install(name, dir); err == nil {
				t.Error("重复安装应报错（不覆盖）")
			}
			// ListInstalled 识别五品类
			installed, err := ListInstalled(dir)
			if err != nil || len(installed) != 1 || installed[0] != name {
				t.Errorf("ListInstalled 异常: %v %v", installed, err)
			}
		})
	}
}

func TestInstall_UnknownKind(t *testing.T) {
	m := testMarketWith(Item{Name: "weird", Kind: "unknown-kind", Description: "d", Content: "x"})
	dir := t.TempDir()
	err := m.Install("weird", dir)
	if err == nil {
		t.Fatal("未知 Kind 应返回错误")
	}
	if !strings.Contains(err.Error(), "未知品类") {
		t.Errorf("错误信息应包含'未知品类': %v", err)
	}
	// 不应残留目录
	if _, statErr := os.Stat(filepath.Join(dir, "weird")); !os.IsNotExist(statErr) {
		t.Error("未知品类不应写入任何文件/目录")
	}
}

func TestInstall_SkillBackwardCompat(t *testing.T) {
	// Kind 为空 → 仍写 SKILL.md（旧数据向后兼容）
	m := testMarketWith(Item{Name: "legacy-skill", Description: "d",
		Content: "---\nname: legacy-skill\n---\nlegacy body"})
	dir := t.TempDir()
	if err := m.Install("legacy-skill", dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "legacy-skill", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "legacy body") {
		t.Errorf("SKILL.md 内容异常: %s", string(data)[:60])
	}
	if err := m.Install("legacy-skill", dir); err == nil {
		t.Error("重复安装应报错")
	}
	// 显式 Kind=skill 同样写 SKILL.md
	m2 := testMarketWith(Item{Name: "explicit-skill", Kind: KindSkill, Description: "d",
		Content: "---\nname: explicit-skill\n---\nexplicit body"})
	if err := m2.Install("explicit-skill", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "explicit-skill", "SKILL.md")); err != nil {
		t.Error("显式 Kind=skill 应写 SKILL.md")
	}
}

func TestSearch_MatchKind(t *testing.T) {
	m := testMarketWith(Item{Name: "ci-toolchain", Kind: KindToolchain,
		Description: "CI 工具链", Content: "x"})
	got := m.Search("toolchain")
	found := false
	for _, it := range got {
		if it.Name == "ci-toolchain" {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(按 Kind 匹配) 应命中 toolchain 条目: %+v", got)
	}
}
