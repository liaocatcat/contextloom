// Package runtime —— 内置种子数据落地（"零配置开箱即用"接线）。
//
// 四个新包体（orchestrator/scenes/toolchain）与技能市场都内置了示例模板，
// 本文件把模板幂等落地到项目 .contextloom/ 目录，让新用户首次启动即可看到
// 可用的员工/场景/工具链/技能，无需任何手工配置：
//
//   - .contextloom/employees/<name>/employee.yaml   6 名内置员工（M3 员工中心）
//   - .contextloom/scenes/<name>/scene.yaml         4 个内置场景（M4 场景中心）
//   - .contextloom/chains/<name>/toolchain.yaml     2 条内置工具链（M2 工具链工厂）
//   - .contextloom/skills/<name>/SKILL.md           8 个内置技能（市场 builtin catalog 导出）
//
// 幂等语义：目标文件已存在即跳过（绝不覆盖用户/GUI 手改的档案）；
// marshal/写入失败只打印 stderr 并继续下一项，绝不阻断 runtime 启动。
// 纯 Go 标准库 + yaml.v3，无新增依赖，跨平台。
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/market"
	"github.com/contextloom/contextloom-agent/internal/orchestrator"
	"github.com/contextloom/contextloom-agent/internal/scenes"
	"github.com/contextloom/contextloom-agent/internal/toolchain"
)

// 种子子目录名（相对 <projRoot>/.contextloom/）。
const (
	seedEmployeesDir = "employees"
	seedScenesDir    = "scenes"
	seedChainsDir    = "chains"
	seedSkillsDir    = "skills"
)

// 各包约定的入口文件名（与对应包 Load 的布局一致）。
const (
	sceneFileName     = "scene.yaml"
	toolchainFileName = "toolchain.yaml"
	skillFileName     = "SKILL.md"
)

// seedBuiltins 幂等落地内置种子数据到 projRoot/.contextloom/ 下（详见文件头注释）。
func seedBuiltins(projRoot string) {
	seedEmployees(filepath.Join(projRoot, ".contextloom", seedEmployeesDir))
	seedScenes(filepath.Join(projRoot, ".contextloom", seedScenesDir))
	seedChains(filepath.Join(projRoot, ".contextloom", seedChainsDir))
	seedSkills(filepath.Join(projRoot, ".contextloom", seedSkillsDir))
}

// seedEmployees 落地员工中心内置 6 名员工档案（含深入本体字段）。
// 同时为每个员工引导工作区目录 + 本体能力说明书（ONTOLOGY.md）——
// 员工本体能力蒸馏的语料源（M3+ 深入本体设计）。
// 迁移语义：已存在的档案绝不覆盖（用户手改/GUI 写入优先，seedFile 幂等）；
// 旧版纯净档案的深入本体字段由 loader 侧模板回退补齐（见 MergeDeepDefaults），
// 无需改动磁盘上的 yaml。
func seedEmployees(dir string) {
	for _, e := range orchestrator.ExampleEmployees() {
		if e.Name == "" {
			continue
		}
		data, err := e.Marshal()
		if err != nil {
			fmt.Fprintf(os.Stderr, "seed: 员工 %q marshal 失败，已跳过: %v\n", e.Name, err)
			continue
		}
		seedFile(filepath.Join(dir, e.Name, orchestrator.EmployeeFileName), data)
		// 工作区本体能力说明书：幂等落地（已存在不覆盖）
		if e.Workspace != "" {
			wsDir := filepath.Join(dir, e.Name, e.Workspace)
			if err := os.MkdirAll(wsDir, 0o755); err == nil {
				doc := filepath.Join(wsDir, orchestrator.DefaultOntologyFileName)
				if _, err := os.Stat(doc); err != nil {
					_ = os.WriteFile(doc, []byte(ontologySeed(e)), 0o644)
				}
			}
		}
	}
}

// ontologySeed 生成员工工作区本体能力说明书的引导内容（首次种子落地用）。
// 引导模板声明员工擅长领域与能力边界，用户可在 ONTOLOGY.md 中继续沉淀
// 参考代码/风格指南/过往成果，实现"本体能力蒸馏"。
func ontologySeed(e orchestrator.Employee) string {
	var b strings.Builder
	b.WriteString("# " + e.Name + " 本体能力说明书\n\n")
	b.WriteString("> 本文件是员工「" + e.Name + "」的本体能力蒸馏载体：执行任务时其内容会注入员工上下文，成为该员工的权威能力引用。\n")
	b.WriteString("> 请持续沉淀：参考代码、风格指南、过往成果、踩坑记录——蒸馏得越充分，员工越像［该岗位的专家］。\n\n")
	b.WriteString("## 岗位\n" + e.Role + "\n")
	if len(e.Expertise) > 0 {
		b.WriteString("\n## 专精领域\n")
		for _, x := range e.Expertise {
			b.WriteString("- " + x + "\n")
		}
	}
	b.WriteString("\n## 能力边界（示例，可编辑）\n- 擅长：\n- 不擅长（转交其他员工）：\n")
	b.WriteString("\n## 风格与偏好（示例，可编辑）\n- \n")
	b.WriteString("\n## 过往成果/参考（示例，可编辑）\n- 参考代码: \n- 风格指南: \n- 经验教训: \n")
	return b.String()
}

// seedScenes 落地场景中心内置 4 个场景。
func seedScenes(dir string) {
	for _, s := range scenes.DefaultBuiltinScenes() {
		if s.Name == "" {
			continue
		}
		data, err := s.Marshal()
		if err != nil {
			fmt.Fprintf(os.Stderr, "seed: 场景 %q marshal 失败，已跳过: %v\n", s.Name, err)
			continue
		}
		seedFile(filepath.Join(dir, s.Name, sceneFileName), data)
	}
}

// seedChains 落地工具链工厂内置示例链。
func seedChains(dir string) {
	for name, c := range toolchain.ExampleChains() {
		if name == "" {
			continue
		}
		data, err := c.Marshal()
		if err != nil {
			fmt.Fprintf(os.Stderr, "seed: 工具链 %q marshal 失败，已跳过: %v\n", name, err)
			continue
		}
		seedFile(filepath.Join(dir, name, toolchainFileName), data)
	}
}

// seedSkills 落地市场内置 8 个技能（SKILL.md 布局与 Market.Install 一致）。
func seedSkills(dir string) {
	for _, it := range market.New().Search("") {
		if it.Source != "builtin" {
			continue // 仅导出内置 catalog；远程条目经 Market.Install 安装
		}
		seedFile(filepath.Join(dir, it.Name, skillFileName), []byte(it.Content))
	}
}

// seedFile 幂等写入：目标已存在则跳过；失败打印 stderr 并继续（静默容忍）。
func seedFile(path string, data []byte) {
	if _, err := os.Stat(path); err == nil {
		return // 已有档案/手改内容优先，绝不覆盖
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "seed: 创建目录 %q 失败，已跳过: %v\n", filepath.Dir(path), err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "seed: 写入 %q 失败，已跳过: %v\n", path, err)
	}
}
