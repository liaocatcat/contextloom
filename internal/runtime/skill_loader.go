package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/contextloom/contextloom-agent/pkg/types"
	"gopkg.in/yaml.v3"
)

// SkillLoader 扫描项目 .contextloom/skills/ 目录加载技能定义。
type SkillLoader struct {
	dir    string                  // 技能目录路径（.contextloom/skills）
	skills map[string]*types.Skill // 已加载技能（name → Skill）
}

// NewSkillLoader 创建技能加载器。
func NewSkillLoader(dir string) *SkillLoader {
	return &SkillLoader{
		dir:    dir,
		skills: make(map[string]*types.Skill),
	}
}

// Load 扫描目录，加载所有 SKILL.md。
// 目录不存在不算错误（返回空列表）。
func (sl *SkillLoader) Load() error {
	entries, err := os.ReadDir(sl.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 目录不存在，静默跳过
		}
		return fmt.Errorf("读取技能目录失败: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(sl.dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue // 无 SKILL.md，跳过
		}
		skill, err := parseSkill(data)
		if err != nil {
			continue // 解析失败，跳过
		}
		skill.Dir = filepath.Join(sl.dir, entry.Name())
		if skill.Name == "" {
			skill.Name = entry.Name()
		}
		sl.skills[skill.Name] = skill
	}
	return nil
}

// List 返回所有已加载技能。
// Dir 返回技能目录路径。
func (sl *SkillLoader) Dir() string { return sl.dir }

func (sl *SkillLoader) List() []*types.Skill {
	result := make([]*types.Skill, 0, len(sl.skills))
	for _, s := range sl.skills {
		result = append(result, s)
	}
	return result
}

// Get 按名称获取技能。
func (sl *SkillLoader) Get(name string) (*types.Skill, bool) {
	s, ok := sl.skills[name]
	return s, ok
}

// parseSkill 解析 SKILL.md 文件（YAML frontmatter + Markdown 正文）。
func parseSkill(data []byte) (*types.Skill, error) {
	content := string(data)

	// 解析 YAML frontmatter（--- 包裹）
	var frontmatter map[string]string
	body := content

	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		// 找到第二个 ---
		rest := content[4:] // 跳过开头 "---\n"
		if strings.HasPrefix(content, "---\r\n") {
			rest = content[5:]
		}
		endIdx := strings.Index(rest, "\n---")
		if endIdx >= 0 {
			fmStr := rest[:endIdx]
			if err := yaml.Unmarshal([]byte(fmStr), &frontmatter); err != nil {
				return nil, fmt.Errorf("解析 frontmatter 失败: %w", err)
			}
			// 正文是第二个 --- 之后的内容
			bodyStart := endIdx + 4 // 跳过 "\n---"
			if bodyStart < len(rest) {
				body = strings.TrimSpace(rest[bodyStart:])
			} else {
				body = ""
			}
		}
	}

	return &types.Skill{
		Name:        frontmatter["name"],
		Description: frontmatter["description"],
		Toolchain:   frontmatter["toolchain"],
		Content:     body,
	}, nil
}
