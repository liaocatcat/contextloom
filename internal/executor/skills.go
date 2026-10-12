package executor

import (
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// Skill 技能定义（多agent实现建议.md §7.1）
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools"`
}

var builtinSkills = map[string]Skill{
	"ANALYZE_PROJECT": {
		Name:        "ANALYZE_PROJECT",
		Description: "分析项目目录结构和关键配置",
		Prompt: "分析项目结构:\n" +
			"1. list_files 项目根目录\n" +
			"2. read_file 关键配置文件(go.mod, config.yaml, Makefile)\n" +
			"3. 输出: 目录树 + 技术栈 + 入口文件",
		Tools: []string{"list_files", "read_file"},
	},
	"RUN_TESTS_REPORT": {
		Name:        "RUN_TESTS_REPORT",
		Description: "运行测试并报告结果",
		Prompt: "运行测试:\n" +
			"1. run_tests 指定目标\n" +
			"2. 如果失败, read_file 失败的测试文件\n" +
			"3. 输出: 通过率 + 失败原因 + 修复建议",
		Tools: []string{"run_tests", "read_file"},
	},
	"FIX_BUG": {
		Name:        "FIX_BUG",
		Description: "修复 bug",
		Prompt: "修复 bug:\n" +
			"1. read_file 目标文件确认问题位置\n" +
			"2. search_content 搜索相关代码\n" +
			"3. replace_in_file 修复\n" +
			"4. run_tests 验证修复",
		Tools: []string{"read_file", "search_content", "replace_in_file", "run_tests"},
	},
	"REFACTOR": {
		Name:        "REFACTOR",
		Description: "重构代码",
		Prompt: "重构代码:\n" +
			"1. read_file 目标文件\n" +
			"2. 分析需要重构的部分\n" +
			"3. write_file/replace_in_file 执行重构\n" +
			"4. run_tests 验证",
		Tools: []string{"read_file", "write_file", "replace_in_file", "run_tests"},
	},
}

var directiveSkillMap = map[string]string{
	"BUG_FIX":  "FIX_BUG",
	"TEST":     "RUN_TESTS_REPORT",
	"EXPLAIN":  "ANALYZE_PROJECT",
	"REFACTOR": "REFACTOR",
}

// injectSkill 根据任务指令注入技能提示（§7.2）
func injectSkill(messages []types.Message, directive string) []types.Message {
	if skillName, ok := directiveSkillMap[directive]; ok {
		if skill, exists := builtinSkills[skillName]; exists {
			messages = append(messages, types.Message{
				Role:    "system",
				Content: "技能指引: " + skill.Prompt,
			})
		}
	}
	return messages
}
