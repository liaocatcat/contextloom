package toolchain

import (
	"context"
	"fmt"
	"regexp"

	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ToolExecutor 工具执行器，由调用方（runtime 用 tools.Registry 包装）注入。
// name 为工具名，params 为已解析（$ref/{{param}} 替换后）的参数。
type ToolExecutor func(name string, params map[string]any) (string, error)

// LLMRunner 大模型执行器，由调用方（runtime 用模型网关包装）注入。
// prompt 为已解析的提示词；artifacts 为当前已注册产物的快照（只读）。
type LLMRunner func(ctx context.Context, prompt string, artifacts map[string]string) (string, error)

// RunOptions 为一次工具链执行的注入配置。
type RunOptions struct {
	// Tools 工具执行器（链含 tool 类步骤时必需）。
	Tools ToolExecutor
	// LLM 模型执行器（链含 llm 类步骤时必需）。
	LLM LLMRunner
	// SubChains 可引用子链注册表，键为链名（链含 ref 子链步骤时必需）。
	SubChains map[string]*Toolchain
	// ParamsRun 链级参数值（{{param.name}} 模板替换源）。
	ParamsRun map[string]string
	// SeedArtifacts 预置产物（子链继承父链产物时使用），键遵循产物协议。
	SeedArtifacts map[string]string
	// MaxFanout 无依赖步骤最大并发数；<=0 时取 DefaultMaxFanout。
	MaxFanout int
}

// StepLogEntry 单步执行日志。
type StepLogEntry struct {
	StepID   string        `json:"step_id" yaml:"step_id"`
	StepType string        `json:"step_type" yaml:"step_type"`
	Status   string        `json:"status" yaml:"status"` // success | failed | skipped
	Error    string        `json:"error,omitempty" yaml:"error,omitempty"`
	Attempts int           `json:"attempts" yaml:"attempts"`
	Duration time.Duration `json:"duration_ms" yaml:"duration_ms"`
	Summary  string        `json:"summary,omitempty" yaml:"summary,omitempty"` // 产物摘要（截断）
}

// 步骤状态常量。
const (
	StepStatusSuccess = "success"
	StepStatusFailed  = "failed"
	StepStatusSkipped = "skipped"
)

// Result 为工具链执行产物协议：
//
//   - Artifacts：两级键——<stepID> 整体产物 + <stepID>.<field> 字段级产物
//     （步骤输出若可解析为 yaml map，其顶层标量字段自动注册为字段级产物）；
//   - Log：每步状态/耗时/产物摘要；
//   - FailedStepID：首个失败步骤 id（无失败为空）；
//   - Skipped：未执行步骤 id 列表（因依赖失败或 ctx 取消）。
type Result struct {
	Artifacts    map[string]string `json:"artifacts" yaml:"artifacts"`
	Log          []StepLogEntry    `json:"log" yaml:"log"`
	FailedStepID string            `json:"failed_step_id,omitempty" yaml:"failed_step_id,omitempty"`
	Skipped      []string          `json:"skipped,omitempty" yaml:"skipped,omitempty"`
}

// DefaultMaxFanout 为无依赖步骤默认最大并发数。
const DefaultMaxFanout = 4

// summaryLimit 为日志产物摘要截断长度。
const summaryLimit = 200

// refTokenRe 匹配产物引用语法：$ref: step.field、$step.field 以及
// 占位模板 {{ts}}、{{param.name}}。
var refTokenRe = regexp.MustCompile(`\$ref:\s*([A-Za-z0-9_][A-Za-z0-9_.-]*)|` +
	`\$([A-Za-z0-9_][A-Za-z0-9_.-]*)|` +
	`\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)

// SplitProductRef 拆分引用目标：
//   - "step.field" → stepID=step, field=field, isProdRef=true；
//   - "step"（无点）→ stepID=step, field="", isProdRef=false（子链名或整体产物）。
func SplitProductRef(ref string) (stepID, field string, isProdRef bool) {
	ref = strings.TrimSpace(ref)
	dot := strings.IndexByte(ref, '.')
	if dot < 0 {
		return ref, "", false
	}
	return ref[:dot], ref[dot+1:], true
}

// resolveText 解析文本中的 $ref / {{param}} / {{ts}} 引用。
// artifacts 为产物注册表；params 为链级参数值。
func resolveText(text string, artifacts map[string]string, params map[string]string) (string, error) {
	var firstErr error
	out := refTokenRe.ReplaceAllStringFunc(text, func(raw string) string {
		if firstErr != nil {
			return raw
		}
		m := refTokenRe.FindStringSubmatch(raw)
		if m == nil {
			return raw
		}
		switch {
		case m[1] != "": // $ref: xxx.yyy 或 $ref: xxx
			target := m[1]
			val, err := lookupArtifact(target, artifacts)
			if err != nil {
				firstErr = err
				return raw
			}
			return val
		case m[2] != "": // $xxx.yyy 缩写
			val, err := lookupArtifact(m[2], artifacts)
			if err != nil {
				firstErr = err
				return raw
			}
			return val
		case m[3] != "": // {{ts}} / {{param.x}}
			return resolveTemplate(m[3], params)
		}
		return raw
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// resolveTemplate 解析 {{name}} 模板：内置 ts 或链级参数。
func resolveTemplate(name string, params map[string]string) string {
	if name == "ts" {
		return time.Now().Format("20060102-150405")
	}
	if v, ok := params[name]; ok {
		return v
	}
	return "" // 未注入参数留空（调用方可用 Validate 前检查必填项）。
}

// lookupArtifact 按产物协议查找引用目标：先精确键，再兼容整体产物（无点）引用。
func lookupArtifact(target string, artifacts map[string]string) (string, error) {
	if v, ok := artifacts[target]; ok {
		return v, nil
	}
	stepID, field, isProd := SplitProductRef(target)
	if isProd {
		return "", fmt.Errorf("$ref target %q: artifact not registered (step %q has no field %q)", target, stepID, field)
	}
	if v, ok := artifacts[stepID]; ok {
		return v, nil
	}
	return "", fmt.Errorf("$ref target %q: artifact not registered", target)
}

// resolveParams 递归解析 params map 中字符串值的引用与模板。
func resolveParams(p map[string]any, artifacts map[string]string, params map[string]string) (map[string]any, error) {
	if p == nil {
		return nil, nil
	}
	out := make(map[string]any, len(p))
	for k, v := range p {
		switch t := v.(type) {
		case string:
			rv, err := resolveText(t, artifacts, params)
			if err != nil {
				return nil, fmt.Errorf("param %q: %w", k, err)
			}
			out[k] = rv
		case map[string]any:
			rv, err := resolveParams(t, artifacts, params)
			if err != nil {
				return nil, fmt.Errorf("param %q: %w", k, err)
			}
			out[k] = rv
		case []any:
			arr := make([]any, 0, len(t))
			for _, item := range t {
				if s, ok := item.(string); ok {
					rv, err := resolveText(s, artifacts, params)
					if err != nil {
						return nil, fmt.Errorf("param %q: %w", k, err)
					}
					arr = append(arr, rv)
				} else {
					arr = append(arr, item)
				}
			}
			out[k] = arr
		default:
			out[k] = v
		}
	}
	return out, nil
}

// registerArtifacts 注册两级产物：<stepID> 整体产物 + <stepID>.<field> 字段级产物。
// 字段级提取：将输出按 yaml map 解析（yaml 为 json 超集，json 输出亦可），
// 顶层标量字段注册为 "<stepID>.<field>"；解析失败（如 git diff 纯文本）仅跳过字段级。
func registerArtifacts(stepID, output string, artifacts map[string]string) {
	artifacts[stepID] = output
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(output), &fields); err != nil || len(fields) == 0 {
		return
	}
	for f, val := range fields {
		if f == "" {
			continue
		}
		key := stepID + "." + f
		switch t := val.(type) {
		case string:
			artifacts[key] = t
		case nil:
			artifacts[key] = ""
		default:
			b, err := yaml.Marshal(val)
			if err == nil {
				artifacts[key] = strings.TrimSuffix(string(b), "\n")
			}
		}
	}
}

// stepRun 为工作 goroutine 的输入/输出。
type stepRun struct {
	idx      int               // 步骤在链中的下标（1-based 编号 = idx+1）
	step     *Step             // 步骤定义
	resolved any               // tool 类: map[string]any；llm 类: string；ref 子链类: map[string]any（可选参数）
	seed     map[string]string // 启动时产物快照（ref 子链继承用）
	output   string
	err      error
	attempts int
	duration time.Duration
}

// runOnce 执行单个步骤一次尝试（不含重试）。resolved 为已解析的参数/提示词。
func runOnce(ctx context.Context, sr *stepRun, opts *RunOptions, artifacts map[string]string) (string, error) {
	s := sr.step
	switch t := s.InferredType(); t {
	case StepTypeTool:
		if opts.Tools == nil {
			return "", fmt.Errorf("no Tools executor injected (step type %q requires it)", t)
		}
		params, _ := sr.resolved.(map[string]any)
		return opts.Tools(s.Tool, params)
	case StepTypeLLM:
		if opts.LLM == nil {
			return "", fmt.Errorf("no LLM runner injected (step type %q requires it)", t)
		}
		prompt, _ := sr.resolved.(string)
		return opts.LLM(ctx, prompt, artifacts)
	case StepTypeRef:
		ref := strings.TrimSpace(s.Ref)
		stepID, _, isProd := SplitProductRef(ref)
		if isProd {
			// 引用上游产物字段：直接取值。
			v, ok := artifacts[ref]
			if !ok {
				return "", fmt.Errorf("$ref %q: artifact not registered", ref)
			}
			return v, nil
		}
		// 引用子链。
		sub := opts.SubChains[stepID]
		if sub == nil {
			return "", fmt.Errorf("sub-chain %q not found in RunOptions.SubChains", stepID)
		}
		subOpts := *opts
		subOpts.SeedArtifacts = sr.seed // 继承父链产物快照
		if params, ok := sr.resolved.(map[string]any); ok && len(params) > 0 {
			pm := make(map[string]string, len(params))
			for pk, pv := range params {
				if pvs, ok := pv.(string); ok {
					pm[pk] = pvs
				}
			}
			subOpts.ParamsRun = pm
		}
		subRes, subErr := Run(ctx, *sub, subOpts)
		if subErr != nil {
			return "", fmt.Errorf("sub-chain %q: %w", stepID, subErr)
		}
		// 子链产物并入父链：子链全部产物以 "<refStepID>.<key>" 前缀注册。
		merged := make(map[string]string, len(subRes.Artifacts)+1)
		for pk, pv := range subRes.Artifacts {
			merged[pk] = pv
		}
		// 输出子链产物（供字段级提取注册 <refStepID>.<key>）；整体产物亦注册为 <refStepID>。
		b, err := yaml.Marshal(merged)
		if err != nil {
			return "", fmt.Errorf("sub-chain %q: marshal artifacts: %w", stepID, err)
		}
		return strings.TrimSuffix(string(b), "\n"), nil
	default:
		return "", fmt.Errorf("unknown step type %q", t)
	}
}

// Run 执行工具链，返回结果与聚合错误（错误非 nil 不代表未执行——失败语义见下）。
//
// 调度语义：
//   - 静态校验不过 → 不执行任何步骤，直接返回校验错误；
//   - 按依赖 DAG 调度：无依赖步骤并行执行（goroutine + 信号量限流 MaxFanout）；
//   - 步骤失败按 Step.Retry 重试整步，仍失败 → 该步 failed，其（传递）依赖者 skipped；
//     不依赖失败步骤的独立分支继续执行；
//   - ctx 取消 → 未启动步骤 skipped，运行中步骤待其返回（tool 执行器无 ctx，
//     取消仅能阻止尚未启动的步骤）；结果与错误仍汇总返回；
//   - 错误为可读聚合：step[2] "review" failed: ...；未执行步骤列表在 err 与 Result.Skipped 中。
func Run(ctx context.Context, chain Toolchain, opts RunOptions) (*Result, error) {
	res := &Result{
		Artifacts: make(map[string]string),
		Log:       make([]StepLogEntry, 0, len(chain.Steps)),
	}

	if errs := Validate(chain, ValidateOptions{KnownChains: namesOf(opts.SubChains)}); len(errs) > 0 {
		return res, fmt.Errorf("toolchain %q validation failed: %s", nameOr(chain.Name), strings.Join(errs, "; "))
	}

	// 必填链级参数检查（能力探针：缺参即给可读错误，不白跑）。
	for _, p := range chain.Params {
		if p.Required && strings.TrimSpace(opts.ParamsRun[p.Name]) == "" {
			return res, fmt.Errorf("toolchain %q: required param %q is missing (values passed via RunOptions.ParamsRun)", nameOr(chain.Name), p.Name)
		}
	}

	n := len(chain.Steps)
	if n == 0 {
		return res, nil
	}

	// 依赖图展开（缺省 = 依赖全部前置步骤）。
	deps := buildDepends(chain)
	byID := make(map[string]int, n)
	for i := range chain.Steps {
		byID[chain.Steps[i].ID] = i
	}
	depCount := make([]int, n)
	dependents := make([][]int, n)
	for i := range chain.Steps {
		for _, d := range deps[chain.Steps[i].ID] {
			j, ok := byID[d]
			if !ok {
				continue // Validate 已报错
			}
			depCount[i]++
			dependents[j] = append(dependents[j], i)
		}
	}

	maxFanout := opts.MaxFanout
	if maxFanout <= 0 {
		maxFanout = DefaultMaxFanout
	}

	artifacts := make(map[string]string, len(opts.SeedArtifacts)+n*4)
	for k, v := range opts.SeedArtifacts {
		artifacts[k] = v
	}

	// 步骤状态：0=pending 1=ready 2=running 3=success 4=failed 5=skipped。
	const (
		stPending = iota
		stReady
		stRunning
		stSuccess
		stFailed
		stSkipped
	)
	status := make([]int, n)
	logs := make([]StepLogEntry, n)
	for i := range chain.Steps {
		logs[i] = StepLogEntry{StepID: chain.Steps[i].ID, StepType: chain.Steps[i].InferredType()}
	}

	type doneMsg struct {
		sr   *stepRun
		fail bool
	}
	doneCh := make(chan doneMsg, n)

	var failedOrder []int // 失败步骤（完成顺序）
	cancelled := false
	running := 0

	// markSkipped 递归标记依赖者未执行（原因：依赖失败或取消）。
	var markSkipped func(i int, reason string)
	markSkipped = func(i int, reason string) {
		for _, j := range dependents[i] {
			if status[j] == stPending || status[j] == stReady {
				status[j] = stSkipped
				logs[j].Status = StepStatusSkipped
				logs[j].Error = reason
				markSkipped(j, reason)
			}
		}
	}

	// spawn 启动一个就绪步骤（调用前 status 必须为 stReady）。
	spawn := func(i int) {
		status[i] = stRunning
		running++
		s := &chain.Steps[i]
		sr := &stepRun{idx: i, step: s}
		seed := make(map[string]string, len(artifacts))
		for k, v := range artifacts {
			seed[k] = v
		}
		sr.seed = seed
		// 主循环内解析（依赖已满足 → 引用目标必已注册）。
		switch s.InferredType() {
		case StepTypeTool:
			p, err := resolveParams(s.Params, artifacts, opts.ParamsRun)
			if err != nil {
				// 参数解析失败 → 直接失败（不进 goroutine）。
				sr.err = err
				sr.output = ""
				sr.attempts = 0
				sr.duration = 0
				doneCh <- doneMsg{sr: sr, fail: true}
				status[i] = stRunning // spawn 逻辑继续由主循环收尾
				return
			}
			sr.resolved = p
		case StepTypeLLM:
			p, err := resolveText(s.Prompt, artifacts, opts.ParamsRun)
			if err != nil {
				sr.err = err
				doneCh <- doneMsg{sr: sr, fail: true}
				return
			}
			if s.RoleHint != "" {
				p = fmt.Sprintf("[角色] %s\n\n%s", s.RoleHint, p)
			}
			sr.resolved = p
		case StepTypeRef:
			if _, _, isProd := SplitProductRef(s.Ref); !isProd {
				// 子链类：解析可选参数。
				p, err := resolveParams(s.Params, artifacts, opts.ParamsRun)
				if err != nil {
					sr.err = err
					doneCh <- doneMsg{sr: sr, fail: true}
					return
				}
				sr.resolved = p
			}
		}
		go func(sr *stepRun) {
			start := time.Now()
			out, err := runOnce(ctx, sr, &opts, sr.seed)
			sr.output = out
			sr.err = err
			sr.attempts = 1
			if err != nil {
				maxA := sr.step.MaxAttempts()
				for a := 2; a <= maxA; a++ {
					out2, err2 := runOnce(ctx, sr, &opts, sr.seed)
					sr.output = out2
					sr.err = err2
					sr.attempts = a
					if err2 == nil {
						break
					}
				}
			}
			sr.duration = time.Since(start)
			doneCh <- doneMsg{sr: sr, fail: sr.err != nil}
		}(sr)
	}

	// 初始就绪：无依赖步骤。
	for i := 0; i < n; i++ {
		if depCount[i] == 0 {
			status[i] = stReady
		}
	}

	for {
		// 推进：启动就绪步骤（限流）。
		if !cancelled {
			for i := 0; i < n && running < maxFanout; i++ {
				if status[i] == stReady {
					spawn(i)
				}
			}
		}

		// 全部终态？
		allDone := true
		for i := 0; i < n; i++ {
			if status[i] != stSuccess && status[i] != stFailed && status[i] != stSkipped {
				allDone = false
				break
			}
		}
		if allDone && running == 0 {
			break
		}

		// 等待完成事件或取消。
		var ctxDone <-chan struct{}
		if !cancelled {
			ctxDone = ctx.Done()
		}
		select {
		case msg := <-doneCh:
			running--
			sr := msg.sr
			i := sr.idx
			if msg.fail {
				status[i] = stFailed
				logs[i].Status = StepStatusFailed
				logs[i].Error = errText(sr.err)
				logs[i].Attempts = sr.attempts
				logs[i].Duration = sr.duration
				failedOrder = append(failedOrder, i)
				markSkipped(i, fmt.Sprintf("dependency step[%d] %q failed", i+1, sr.step.ID))
			} else {
				status[i] = stSuccess
				logs[i].Status = StepStatusSuccess
				logs[i].Attempts = sr.attempts
				logs[i].Duration = sr.duration
				logs[i].Summary = summarize(sr.output)
				registerArtifacts(sr.step.ID, sr.output, artifacts)
				// 下游依赖解除。
				for _, j := range dependents[i] {
					depCount[j]--
					if depCount[j] == 0 && status[j] == stPending {
						status[j] = stReady
					}
				}
			}
		case <-ctxDone:
			cancelled = true
			for i := 0; i < n; i++ {
				if status[i] == stPending || status[i] == stReady {
					status[i] = stSkipped
					logs[i].Status = StepStatusSkipped
					logs[i].Error = "cancelled: " + ctx.Err().Error()
				}
			}
		}
	}

	// 组装结果。
	for i := 0; i < n; i++ {
		res.Log = append(res.Log, logs[i])
	}
	for k, v := range artifacts {
		res.Artifacts[k] = v
	}
	var errs []string
	for _, i := range failedOrder {
		if res.FailedStepID == "" {
			res.FailedStepID = chain.Steps[i].ID
		}
		errs = append(errs, fmt.Sprintf("step[%d] %q failed: %s", i+1, chain.Steps[i].ID, logs[i].Error))
	}
	for i := 0; i < n; i++ {
		if status[i] == stSkipped {
			res.Skipped = append(res.Skipped, chain.Steps[i].ID)
		}
	}
	_ = failedOrder
	if len(errs) > 0 {
		err := fmt.Errorf("%s", strings.Join(errs, "; "))
		if len(res.Skipped) > 0 {
			err = fmt.Errorf("%s; unexecuted steps: [%s]", err, strings.Join(res.Skipped, ", "))
		}
		return res, err
	}
	if cancelled && ctx.Err() != nil {
		if len(res.Skipped) > 0 {
			return res, fmt.Errorf("execution cancelled: %v; unexecuted steps: [%s]", ctx.Err(), strings.Join(res.Skipped, ", "))
		}
		return res, fmt.Errorf("execution cancelled: %w", ctx.Err())
	}
	return res, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func summarize(out string) string {
	if len(out) <= summaryLimit {
		return out
	}
	return out[:summaryLimit] + "…"
}

// namesOf 提取子链注册表名称集合（供 Validate 校验已知子链）。
func namesOf(m map[string]*Toolchain) map[string]bool {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]bool, len(m))
	for name, c := range m {
		if c != nil {
			out[name] = true
		}
	}
	return out
}
