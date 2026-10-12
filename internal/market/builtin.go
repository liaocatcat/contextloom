package market

// builtinCatalog 内置 8 个示例技能（SKILL.md 格式：YAML frontmatter + Markdown 正文）。
func builtinCatalog() Catalog {
	return Catalog{
		Name: "builtin",
		Items: []Item{
			{
				Name: "code-review", Version: "1.0.0", Source: "builtin", Author: "ContextLoom",
				Description: "代码审查：检查正确性/安全/性能/风格，输出分级问题清单",
				Tags:        []string{"review", "quality", "audit"},
				Content: `---
name: code-review
description: 代码审查：检查正确性/安全/性能/风格，输出分级问题清单
---
# 代码审查

对目标代码进行系统审查，按以下维度输出：

## 审查维度
1. **正确性**：逻辑错误、边界条件、并发安全
2. **安全性**：注入、路径穿越、敏感信息泄露、命令执行风险
3. **性能**：明显低效的算法/循环、不必要的分配
4. **可维护性**：命名、重复代码、过度复杂

## 输出格式
- 每个问题：[级别] 文件:行 - 问题描述 - 修复建议
- 级别：🔴 严重 / 🟡 建议 / ⚪ 风格
- 结尾给出总体评价（1-10 分）
`,
			},
			{
				Name: "test-writer", Version: "1.0.0", Source: "builtin", Author: "ContextLoom",
				Description: "测试编写：为指定函数/模块生成单元测试（表驱动+边界+错误路径）",
				Tags:        []string{"test", "quality"},
				Content: `---
name: test-writer
description: 测试编写：为指定函数/模块生成单元测试
---
# 测试编写

为目标代码生成高质量单测：

## 要求
1. 表驱动测试（cases 数组）
2. 覆盖：正常路径、边界值、空输入、错误路径
3. 测试名有语义（TestXxx_Scenario）
4. 断言清晰，不吞错误

## 输出
- 完整测试文件内容（可直接写入 *_test.go）
- 说明每个用例覆盖的场景
`,
			},
			{
				Name: "refactor", Version: "1.0.0", Source: "builtin", Author: "ContextLoom",
				Description: "代码重构：识别坏味道，保持行为不变地改进结构",
				Tags:        []string{"refactor", "clean"},
				Content: `---
name: refactor
description: 代码重构：识别坏味道，保持行为不变地改进结构
---
# 代码重构

在不改变外部行为的前提下改进代码结构：

## 步骤
1. 识别坏味道（长函数、重复、深嵌套、全局状态）
2. 提出重构方案（提取函数/消除重复/简化条件）
3. 小步实施，每步可编译可测试
4. 重构后运行测试确认无回归

## 纪律
- 重构 ≠ 加功能：不改变行为
- 一次只做一个重构模式
`,
			},
			{
				Name: "doc-gen", Version: "1.0.0", Source: "builtin", Author: "ContextLoom",
				Description: "文档生成：为代码生成 README/API 文档/注释说明",
				Tags:        []string{"doc", "readme"},
				Content: `---
name: doc-gen
description: 文档生成：为代码生成 README/API 文档/注释说明
---
# 文档生成

为指定代码/模块生成用户与维护文档：

## 内容
1. 功能概述（一句话 + 详细说明）
2. 安装/使用示例（可运行代码块）
3. 配置项说明（表格）
4. API 参考（签名 + 参数 + 返回值 + 示例）
5. 常见问题

## 风格
- 中文为主，术语保留英文
- 示例必须真实可运行
`,
			},
			{
				Name: "bug-hunter", Version: "1.0.0", Source: "builtin", Author: "ContextLoom",
				Description: "缺陷定位：根据错误信息/现象定位根因并给出修复",
				Tags:        []string{"bug", "debug", "fix"},
				Content: `---
name: bug-hunter
description: 缺陷定位：根据错误信息/现象定位根因并给出修复
---
# 缺陷定位

根据用户提供的错误信息/现象，系统性定位根因：

## 方法
1. 复现：构造最小复现路径
2. 二分：缩小可疑范围（输入/状态/依赖）
3. 证据链：每个结论必须有代码/日志证据
4. 根因 vs 表象：区分真正的根因与连锁反应

## 输出
- 根因（一句话 + 证据）
- 修复方案（最小改动）
- 回归测试建议
`,
			},
			{
				Name: "perf-audit", Version: "1.0.0", Source: "builtin", Author: "ContextLoom",
				Description: "性能审计：分析热点、内存与并发瓶颈，给出优化建议",
				Tags:        []string{"perf", "performance", "optimize"},
				Content: `---
name: perf-audit
description: 性能审计：分析热点、内存与并发瓶颈，给出优化建议
---
# 性能审计

对目标代码进行性能分析：

## 维度
1. **CPU 热点**：算法复杂度、循环内重复计算
2. **内存**：不必要的分配、大对象持有、缓存策略
3. **并发**：锁竞争、goroutine 泄漏、扇出扇入
4. **I/O**：重复读盘、未批量、连接未复用

## 输出
- 每个问题：影响面（量级估计）+ 优化方案 + 预期收益
- 标注哪些是"值得优化"哪些是"过度优化"
`,
			},
			{
				Name: "security-scan", Version: "1.0.0", Source: "builtin", Author: "ContextLoom",
				Description: "安全扫描：检查密钥泄露/注入/路径穿越/危险函数",
				Tags:        []string{"security", "scan", "audit"},
				Content: `---
name: security-scan
description: 安全扫描：检查密钥泄露/注入/路径穿越/危险函数
---
# 安全扫描

对目标代码/配置进行安全扫描：

## 检查项
1. **密钥泄露**：硬编码 API Key/密码/token（含历史）
2. **注入**：命令拼接、SQL 拼接、模板注入
3. **路径**：路径穿越、未校验的绝对路径写入
4. **危险函数**：eval、exec 未净化、反序列化
5. **依赖**：已知漏洞的高危依赖

## 输出
- 每个发现：[级别] 位置 - 风险描述 - 修复建议
- 级别：🔴 高危 / 🟡 中危 / ⚪ 低危
`,
			},
			{
				Name: "migration-helper", Version: "1.0.0", Source: "builtin", Author: "ContextLoom",
				Description: "迁移辅助：语言/框架/API 升级迁移方案与执行",
				Tags:        []string{"migration", "upgrade"},
				Content: `---
name: migration-helper
description: 迁移辅助：语言/框架/API 升级迁移方案与执行
---
# 迁移辅助

协助完成代码迁移（语言/框架/API 升级）：

## 流程
1. 盘点：列出所有受影响位置（调用点/配置/依赖）
2. 映射表：旧 API → 新 API（逐条对应）
3. 分批迁移：每批可编译、可测试、可回滚
4. 兼容层：必要时提供过渡 shim

## 输出
- 迁移清单（含检查状态）
- 每批的改动说明与验证方式
`,
			},
		},
	}
}
