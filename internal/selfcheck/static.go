// Package selfcheck 实现「工具链体系升级方案」§4.6 的质量自我进化体系
// 三件套：质疑有主体（静态核查器）、证据要实测（测试 TEE 发证）、
// 不过不外显（强制门禁）。全部基于标准库与 gopkg.in/yaml.v3，
// go.mod 零新增依赖；编译/测试执行与模型调用均通过函数注入，
// 与 runtime/goalmgr/executor/overseer/toolchain 完全解耦。
package selfcheck

import (
	"fmt"
	"regexp"
	"strings"
)

// FileChange 一次改动中的一个文件（静态核查输入）。
type FileChange struct {
	Path    string // 相对项目根路径
	Content string // 文件内容
}

// Issue 一条核查发现的问题。
type Issue struct {
	File     string // 命中文件路径
	Line     int    // 行号（1 起；跨行扫描无精确行号时为 0）
	Kind     string // 问题类别：residue / unresolved / unused / ...
	Hint     string // 可读提示
	Severity string // high / medium / low
}

// StaticReport 静态核查报告。
type StaticReport struct {
	Issues   []Issue `yaml:"issues"`
	Pass     bool    `yaml:"pass"`
	Scanned  int     `yaml:"scanned"` // 扫描文件数
	PassRate float64 `yaml:"-"`
}

// 残留标记模式：TODO/FIXME/panic("TODO")/临时文件/mock 残留/.tmp/.bak 等。
var residuePatterns = []struct {
	re       *regexp.Regexp
	kind     string
	severity string
}{
	{regexp.MustCompile(`(?i)\bTODO\b`), "residue", "low"},
	{regexp.MustCompile(`(?i)\bFIXME\b`), "residue", "medium"},
	{regexp.MustCompile(`(?i)\bXXX\b`), "residue", "medium"},
	{regexp.MustCompile(`panic\(\s*"TODO"`), "residue", "high"},
	{regexp.MustCompile(`(?i)\bHACK\b`), "residue", "medium"},
	{regexp.MustCompile(`(?i)临时文件|临时方案|占位实现|placeholder implementation`), "residue", "medium"},
	{regexp.MustCompile(`(?i)\bmock\b.*残留|残留.*\bmock\b`), "residue", "medium"},
	{regexp.MustCompile(`\.tmp\b|\.bak\b|\.swp\b|~$`), "residue", "low"},
	{regexp.MustCompile(`debug\.Print|fmt\.Println\("DEBUG|console\.log\(`), "residue", "medium"},
}

// unresolvedPattern 未决标记：[?]（提示词层强制模型对拿不准处打标）。
var unresolvedPattern = regexp.MustCompile(`\[\?\]`)

// symbolPattern 声明类符号（Go 风格：var/const/type/func 标识符提取）。
var goDeclPattern = regexp.MustCompile(`(?m)^\s*(?:var|const|type|func)\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:\[[^\]]*\]|\(|struct|\*|[A-Za-z_])?`)

// StaticOptions 静态核查配置（全部可选，零值=默认全开）。
type StaticOptions struct {
	// ScanResidue 扫描残留标记（默认 true）。
	ScanResidue *bool
	// ScanUnresolved 扫描未决标记 [?]（默认 true）。
	ScanUnresolved *bool
	// KnownSymbols 项目内已声明符号集合（用于未用符号闭锁核查；
	// 为空则跳过该核查）。
	KnownSymbols map[string]bool
	// DeclaredSymbols 本次改动中声明的符号（与 KnownSymbols 求差：
	// 声明了但项目内未见使用 → 报 unused）。为空则跳过。
	DeclaredSymbols map[string]bool
}

// StaticChecker 静态核查器（质疑引擎的确定性武器，零模型依赖）。
type StaticChecker struct {
	opts StaticOptions
}

// NewStaticChecker 构造核查器。
func NewStaticChecker(opts StaticOptions) *StaticChecker {
	return &StaticChecker{opts: opts}
}

// Check 扫描改动文件清单，产出报告。Pass 仅当零 high/medium 级别问题。
func (sc *StaticChecker) Check(files []FileChange) StaticReport {
	rep := StaticReport{Scanned: len(files)}
	scanResidue := sc.opts.ScanResidue == nil || *sc.opts.ScanResidue
	scanUnresolved := sc.opts.ScanUnresolved == nil || *sc.opts.ScanUnresolved

	for _, f := range files {
		if scanResidue {
			rep.Issues = append(rep.Issues, scanFileResidues(f)...)
		}
		if scanUnresolved {
			rep.Issues = append(rep.Issues, scanUnresolvedMarkers(f)...)
		}
	}
	// 闭锁核查：本次声明了但项目内未使用的符号
	rep.Issues = append(rep.Issues, sc.checkUnusedSymbols(files)...)

	rep.Pass = true
	for _, it := range rep.Issues {
		if it.Severity == "high" || it.Severity == "medium" {
			rep.Pass = false
			break
		}
	}
	if len(rep.Issues) > 0 {
		rep.PassRate = float64(countPassable(rep.Issues)) / float64(len(rep.Issues))
	}
	return rep
}

// countPassable low 级别问题数（可豁免）。
func countPassable(issues []Issue) int {
	n := 0
	for _, it := range issues {
		if it.Severity == "low" {
			n++
		}
	}
	return n
}

// scanFileResidues 逐行扫描残留标记。
func scanFileResidues(f FileChange) []Issue {
	var out []Issue
	lines := strings.Split(f.Content, "\n")
	for i, line := range lines {
		for _, p := range residuePatterns {
			if p.re.MatchString(line) {
				out = append(out, Issue{
					File:     f.Path,
					Line:     i + 1,
					Kind:     p.kind,
					Severity: p.severity,
					Hint:     fmt.Sprintf("检测到残留标记 %q：%s", p.re.FindString(line), strings.TrimSpace(line)),
				})
			}
		}
	}
	return out
}

// scanUnresolvedMarkers 扫描未决标记 [?]：拿不准处必须解决后才能交付。
func scanUnresolvedMarkers(f FileChange) []Issue {
	var out []Issue
	lines := strings.Split(f.Content, "\n")
	for i, line := range lines {
		if unresolvedPattern.MatchString(line) {
			out = append(out, Issue{
				File:     f.Path,
				Line:     i + 1,
				Kind:     "unresolved",
				Severity: "high",
				Hint:     fmt.Sprintf("存在未决标记 [?]：%s", strings.TrimSpace(line)),
			})
		}
	}
	return out
}

// checkUnusedSymbols 闭锁核查：本次声明但项目内未使用的符号。
func (sc *StaticChecker) checkUnusedSymbols(files []FileChange) []Issue {
	if len(sc.opts.DeclaredSymbols) == 0 || len(sc.opts.KnownSymbols) == 0 {
		return nil
	}
	var out []Issue
	// 声明位置来自改动文件；使用判断：KnownSymbols（项目既有符号表）。
	for sym := range sc.opts.DeclaredSymbols {
		if sc.opts.KnownSymbols[sym] {
			continue // 项目内存在使用/引用
		}
		// 定位声明文件/行（首个命中即可）
		file, line := locateSymbol(files, sym)
		out = append(out, Issue{
			File:     file,
			Line:     line,
			Kind:     "unused",
			Severity: "medium",
			Hint:     fmt.Sprintf("符号 %q 已声明但项目内未见使用（闭锁核查）", sym),
		})
	}
	return out
}

// locateSymbol 在改动文件中定位符号声明位置。
func locateSymbol(files []FileChange, sym string) (string, int) {
	for _, f := range files {
		lines := strings.Split(f.Content, "\n")
		for i, line := range lines {
			if strings.Contains(line, sym) && goDeclPattern.MatchString(line) {
				if m := goDeclPattern.FindStringSubmatch(line); len(m) > 1 && m[1] == sym {
					return f.Path, i + 1
				}
			}
		}
	}
	return "", 0
}

// String 报告的可读摘要（供缺陷清单注入提示词）。
func (r StaticReport) String() string {
	if r.Pass {
		return fmt.Sprintf("静态核查通过（%d 文件，%d 项低危提示）", r.Scanned, len(r.Issues))
	}
	return fmt.Sprintf("静态核查不通过（%d 文件，%d 项问题）", r.Scanned, len(r.Issues))
}
