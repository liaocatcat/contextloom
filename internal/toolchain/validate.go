package toolchain

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateOptions 为静态校验提供调用方已知的工具集/子链集（可为空）。
// 已知集为空 map 时跳过对应检查（例如子链在 Run 时注入，校验时可能尚不可知）。
type ValidateOptions struct {
	// KnownTools 已知工具名集合；非 nil 时校验 tool 类步骤的 Tool 是否在其中。
	KnownTools map[string]bool
	// KnownChains 已知子链名集合；非 nil 时校验 ref 类步骤引用的子链是否在其中。
	KnownChains map[string]bool
}

// Validate 静态校验一条工具链，返回可读错误列表（空 = 通过）。
// 覆盖：链名/步骤缺失、id 为空或重复、id 含 '.'（破坏 <stepID>.<field> 产物协议）、
// 未知步骤类型、tool/llm/ref 各类缺失必填字段、depends_on 缺失依赖、
// 依赖环（含缺省"依赖全部前置"展开与 $ref 隐式依赖形成的环）、
// retry 非法值、未知工具名/子链名。
func Validate(c Toolchain, opts ValidateOptions) []string {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	name := strings.TrimSpace(c.Name)
	if name == "" {
		add("toolchain name is empty")
	}
	if len(c.Steps) == 0 {
		add("toolchain %q has no steps", nameOr(name))
		return errs
	}

	byID := map[string]int{}
	for i := range c.Steps {
		s := &c.Steps[i]
		if strings.TrimSpace(s.ID) == "" {
			add("step[%d]: id is empty", i+1)
			continue
		}
		if strings.Contains(s.ID, ".") {
			add("step[%d]: id %q must not contain '.' (reserved for <stepID>.<field> artifact keys)", i+1, s.ID)
		}
		if j, dup := byID[s.ID]; dup {
			add("step[%d]: duplicate step id %q (already used by step[%d])", i+1, s.ID, j+1)
			continue
		}
		byID[s.ID] = i

		switch t := s.InferredType(); t {
		case StepTypeTool:
			if strings.TrimSpace(s.Tool) == "" {
				add("step[%d] %q: tool step requires a non-empty tool name", i+1, s.ID)
			} else if opts.KnownTools != nil && !opts.KnownTools[s.Tool] {
				add("step[%d] %q: unknown tool %q", i+1, s.ID, s.Tool)
			}
		case StepTypeLLM:
			if strings.TrimSpace(s.Prompt) == "" {
				add("step[%d] %q: llm step requires a non-empty prompt", i+1, s.ID)
			}
		case StepTypeRef:
			if strings.TrimSpace(s.Ref) == "" {
				add("step[%d] %q: ref step requires a non-empty ref target", i+1, s.ID)
			} else if _, _, isProd := SplitProductRef(s.Ref); !isProd {
				// 子链引用：校验已知子链集（若提供）。
				if opts.KnownChains != nil && !opts.KnownChains[s.Ref] {
					add("step[%d] %q: unknown sub-chain %q", i+1, s.ID, s.Ref)
				}
			}
			// 产物引用（含点）不做存在性校验：目标可能是父链 seed 产物，
			// 执行时解析失败会给出可读错误（宽松校验以支持跨链引用）。
		default:
			if t == "" {
				add("step[%d] %q: cannot infer step type (need type, tool, prompt or ref)", i+1, s.ID)
			} else {
				add("step[%d] %q: unknown step type %q (want tool|llm|ref)", i+1, s.ID, t)
			}
		}

		if s.Retry < 0 {
			add("step[%d] %q: retry must be >= 0, got %d", i+1, s.ID, s.Retry)
		}

		// 显式 depends_on 引用未知步骤。
		for _, d := range s.DependsOn {
			if _, ok := byID[d]; !ok {
				add("step[%d] %q: depends_on %q is not a known step id", i+1, s.ID, d)
			}
		}
	}

	// 环检测（含自环）。对"缺省依赖全部前置"的链，天然无环；显式 depends_on
	// 与 $ref 隐式依赖可能成环。
	deps := buildDepends(c)
	for _, cyc := range findCycles(c, deps) {
		add("dependency cycle detected: %s", strings.Join(cyc, " -> "))
	}

	return errs
}

// collectRefs 提取步骤中引用的产物目标 id（params 值、prompt 中的
// "$ref: x.field" / "$x.field"，以及 ref 类步骤带点的目标），
// 用于把产物依赖自动并入步骤依赖（保证引用目标先执行）。
func collectRefs(s *Step) []string {
	seen := map[string]bool{}
	var out []string
	add := func(stepID string) {
		if stepID != "" && stepID != s.ID && !seen[stepID] {
			seen[stepID] = true
			out = append(out, stepID)
		}
	}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			for _, m := range refTokenRe.FindAllStringSubmatch(t, -1) {
				if m[1] != "" { // $ref: x.y
					id, _, _ := SplitProductRef(m[1])
					add(id)
				} else if m[2] != "" { // $x.y
					id, _, _ := SplitProductRef(m[2])
					add(id)
				}
			}
		case map[string]any:
			for _, vv := range t {
				walk(vv)
			}
		case []any:
			for _, vv := range t {
				walk(vv)
			}
		}
	}
	walk(s.Params)
	walk(s.Prompt)
	if _, _, isProd := SplitProductRef(strings.TrimSpace(s.Ref)); isProd {
		id, _, _ := SplitProductRef(strings.TrimSpace(s.Ref))
		add(id)
	}
	return out
}

// buildDepends 展开每个步骤的依赖：
//   - DependsOn 为 nil：缺省 = 依赖链中全部前置步骤；
//   - 显式空数组：无依赖；
//   - 非空列表：原样；
//   - 附加：params/prompt/$ref 中引用的链内步骤 id 自动并入（隐式依赖，去重）。
func buildDepends(c Toolchain) map[string][]string {
	out := make(map[string][]string, len(c.Steps))
	known := make(map[string]bool, len(c.Steps))
	for i := range c.Steps {
		known[c.Steps[i].ID] = true
	}
	prefix := make([]string, 0, len(c.Steps))
	for i := range c.Steps {
		s := &c.Steps[i]
		var ds []string
		if s.DependsOn == nil {
			ds = append([]string(nil), prefix...)
		} else {
			ds = append(ds, s.DependsOn...)
		}
		// 隐式 $ref 依赖（仅链内步骤，跨链 seed 目标不参与）。
		for _, r := range collectRefs(s) {
			if known[r] {
				ds = append(ds, r)
			}
		}
		out[s.ID] = dedup(ds)
		prefix = append(prefix, s.ID)
	}
	return out
}

// findCycles 返回依赖图中的环路径（每个环一条），无环返回 nil。
// 环路径形如 ["a","b","a"]（末元素重复首元素，表示回到起点）。
func findCycles(c Toolchain, deps map[string][]string) [][]string {
	index := map[string]int{}
	for i := range c.Steps {
		index[c.Steps[i].ID] = i
	}
	const (
		white = 0 // 未访问
		gray  = 1 // 访问中（栈上）
		black = 2 // 已完成
	)
	color := make([]int, len(c.Steps))
	stack := []string{}
	var cycles [][]string

	var dfs func(string)
	dfs = func(id string) {
		idx := index[id]
		color[idx] = gray
		stack = append(stack, id)
		for _, d := range deps[id] {
			di, ok := index[d]
			if !ok {
				continue // 缺失依赖已在 Validate 中另报
			}
			switch color[di] {
			case white:
				dfs(d)
			case gray:
				// 找到环：截取 stack 中从 d 开始的部分。
				for k, nameIn := range stack {
					if nameIn == d {
						cyc := append([]string(nil), stack[k:]...)
						cyc = append(cyc, d)
						cycles = append(cycles, cyc)
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[idx] = black
	}
	for _, s := range c.Steps {
		if color[index[s.ID]] == white {
			dfs(s.ID)
		}
	}
	sort.Slice(cycles, func(i, j int) bool {
		return strings.Join(cycles[i], "->") < strings.Join(cycles[j], "->")
	})
	return cycles
}

func dedup(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
