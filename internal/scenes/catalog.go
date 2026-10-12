package scenes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// LoadAll 扫描场景目录 dir（通常为 .contextloom/scenes/），加载其中全部场景：
//
//   - 只遍历 dir 的直接子目录，每个子目录 = 一个场景，入口为 <子目录>/scene.yaml；
//   - 不含 scene.yaml 的子目录、普通文件一律跳过（静默忽略，不视为错误）；
//   - 加载失败的场景返回错误并定位到对应子目录。
//
// 返回按 name 升序排序的场景列表，保证 Match 的确定性优先遍历顺序稳定。
func LoadAll(dir string) ([]Scene, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scenes: 扫描场景目录 %q 失败：%w", dir, err)
	}

	var scenes []Scene
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		sc, err := Load(sub)
		if err != nil {
			// Load 用 %w 包裹底层错误，须用 errors.Is 而非 os.IsNotExist
			// 识别"子目录不存在 scene.yaml"（如普通资源目录），静默跳过。
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("scenes: 加载场景 %q 失败：%w", e.Name(), err)
		}
		scenes = append(scenes, *sc)
	}

	sort.SliceStable(scenes, func(i, j int) bool { return scenes[i].Name < scenes[j].Name })
	return scenes, nil
}

// DefaultBuiltinScenes 返回内置四场景（随发行、开箱即用），对应方案 §4.4.2：
//
//   - weekly-report 办公周报（office 场景）
//   - pr-review 开发 PR 审查（dev 场景）
//   - prd 产品 PRD（product 场景）
//   - post 文案推文（copywriting 场景）
//
// 四场景 keywords 彼此无交集，保证规则层确定性匹配不歧义。
func DefaultBuiltinScenes() []Scene {
	return []Scene{
		{
			Name:         "weekly-report",
			Display:      "📊 周报生成",
			Keywords:     []string{"周报", "周总结", "weekly"},
			IntentPrompt: "判断用户是否要写周报",
			Team:         "report-team",
			Models:       ModelsPolicy{Policy: "budget=low"},
			Toolchain:    "weekly-report-chain",
			Approval:     "standard",
			Output:       OutputSpec{Path: "docs/weekly/{{date}}.md", AutoLoad: true},
			Verify:       "overseer",
			Triggers:     TriggersSpec{Cron: "0 17 * * 5"},
		},
		{
			Name:    "pr-review",
			Display: "🔍 PR 审查报告",
			// 注意：不含裸 "pr" 关键词——"pr" 是 "prd" 的子串，会造成
			// "帮我写 PRD" 被规则层误命中 pr-review，故用更精确的短语。
			Keywords:     []string{"review", "审查", "代码审查", "pull request"},
			IntentPrompt: "判断用户是否需要出 PR 审查报告",
			Team:         "review-team",
			Models:       ModelsPolicy{Policy: "role=coder"},
			Toolchain:    "code-review-chain",
			Approval:     "standard",
			Output:       OutputSpec{Path: "docs/review/{{date}}.md", AutoLoad: true},
			Verify:       "overseer",
		},
		{
			Name:         "prd",
			Display:      "📝 PRD 生成",
			Keywords:     []string{"prd", "需求文档", "产品文档"},
			IntentPrompt: "判断用户是否要写产品需求文档 PRD",
			Team:         "product-team",
			Models:       ModelsPolicy{Policy: "role=product"},
			Toolchain:    "prd-chain",
			Approval:     "permissive",
			Output:       OutputSpec{Path: "docs/prd/{{date}}.md", AutoLoad: true},
			Verify:       "overseer",
		},
		{
			Name:         "post",
			Display:      "✍️ 文案推文",
			Keywords:     []string{"推文", "文案", "公众号", "tweet"},
			IntentPrompt: "判断用户是否要写推文或品牌文案",
			Team:         "writer-team",
			Models:       ModelsPolicy{Policy: "budget=low"},
			Toolchain:    "copywriting-chain",
			Approval:     "permissive",
			Output:       OutputSpec{Path: "docs/post/{{date}}.md", AutoLoad: true},
			Verify:       "overseer",
		},
	}
}
