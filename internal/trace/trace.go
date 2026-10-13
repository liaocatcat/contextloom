// Package trace 提供轻量可观测性（对标前沿 OTel/tracing 的零依赖 JSONL 实现）。
//
// 设计原则：
//   - 单文件追加写 JSONL，按日轮转（trace-20060102.jsonl），零外部依赖；
//   - Record 永不阻塞主流程（失败静默降级，不影响 agent 执行）；
//   - 记录点：agent 每轮迭代、工具调用耗时、goal 轮次、错误——用于多轮失败归因
//     与"改动后能力退化"的可观测（评估回归的原始数据源）。
package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// 事件类型常量（Event 字段取值；Entry 结构不变）。
// EventThinkRound 思考轮事件（M11 度量）：多轮思考机制每登记一轮写入一条。
// Detail 携带调用方序列化的 thinkmgr.ThinkRound JSON（round/kind/has_tools/
// tool_n/body_len/reason）——trace 包不依赖 thinkmgr（避免依赖倒置），
// detail 由调用方序列化为字符串传入。
const EventThinkRound = "think_round"

// Entry 单条 trace 记录（JSON 行）。
type Entry struct {
	TS      time.Time `json:"ts"`                // 记录时间
	Session string    `json:"session,omitempty"` // 会话 ID（前 8 位）
	Mode    string    `json:"mode,omitempty"`    // chat / task / goal
	Round   int       `json:"round,omitempty"`   // agent 迭代轮次
	Event   string    `json:"event"`             // agent_round / tool_call / goal_round / think_round / error
	Tool    string    `json:"tool,omitempty"`    // 工具名
	Ms      int64     `json:"ms,omitempty"`      // 耗时毫秒
	Tokens  int       `json:"tokens,omitempty"`  // token 估算（尽力而为）
	Err     string    `json:"err,omitempty"`     // 错误信息
	Detail  string    `json:"detail,omitempty"`  // 附加说明（如 goal 评估分）
}

// Recorder JSONL trace 记录器。
type Recorder struct {
	mu      sync.Mutex
	dir     string
	enabled bool
	file    *os.File
	day     string // 当前打开文件的日期 20060102
	maxKeep int    // 保留最近 N 个日轮转文件（0=不清理）
	sink    Sink   // 导出目标（如 OTLP Exporter；nil=不导出）
}

// Sink 导出目标接口：Enqueue 每条记录（实现方必须非阻塞），
// Flush 收尾冲刷（Recorder.Close 时调用；无缓冲实现返回 nil）。
type Sink interface {
	Enqueue(Entry)
	Flush() error
}

// New 创建 Recorder。enabled=false 时所有操作为空操作（不创建目录/文件）。
func New(dir string, enabled bool) (*Recorder, error) {
	r := &Recorder{dir: dir, enabled: enabled, maxKeep: 30}
	if !enabled {
		return r, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 trace 目录失败: %w", err)
	}
	if err := r.rotateLocked(time.Now()); err != nil {
		return nil, err
	}
	return r, nil
}

// rotateLocked 确保 file 对应 day 的日志文件（调用方须持有 mu）。
func (r *Recorder) rotateLocked(now time.Time) error {
	day := now.Format("20060102")
	if r.file != nil && r.day == day {
		return nil
	}
	if r.file != nil {
		r.file.Close()
		r.file = nil
	}
	f, err := os.OpenFile(filepath.Join(r.dir, "trace-"+day+".jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	r.file = f
	r.day = day
	r.pruneLocked()
	return nil
}

// pruneLocked 清理超过 maxKeep 的旧日轮转文件（文件名 trace-YYYYMMDD.jsonl，
// 字典序即日期序）。trace 只在 rotate 时触发，避免启动路径额外开销；
// 保留 30 天（与 audit 对齐），防止长运行项目 trace 目录无限累积。
func (r *Recorder) pruneLocked() {
	if r.maxKeep <= 0 {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(r.dir, "trace-*.jsonl"))
	if len(matches) <= r.maxKeep {
		return
	}
	sort.Strings(matches)
	for _, f := range matches[:len(matches)-r.maxKeep] {
		os.Remove(f) //nolint:errcheck // 清理失败不阻塞
	}
}

// Record 追加一条记录。失败静默（trace 不应影响主流程）。
func (r *Recorder) Record(e Entry) {
	if !r.enabled {
		return
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	r.mu.Lock()
	if err := r.rotateLocked(e.TS); err != nil {
		r.mu.Unlock()
		return
	}
	r.file.Write(append(data, '\n'))
	sink := r.sink
	r.mu.Unlock()
	if sink != nil {
		sink.Enqueue(e) // 同步调用；实现方（Exporter.Enqueue）非阻塞
	}
}

// TraceThinkRound 记录一条思考轮事件（M11 便捷方法）：写 EventThinkRound，
// Detail 由调用方序列化传入（thinkmgr.ThinkRound 的 JSON；trace 不 import
// thinkmgr，避免依赖倒置）。失败静默，与 Record 语义一致；enabled=false 时
// 零操作（零回归）。
func (r *Recorder) TraceThinkRound(mode string, round int, detail string) {
	r.Record(Entry{Event: EventThinkRound, Mode: mode, Round: round, Detail: detail})
}

// SetSink 注册导出目标（如 *Exporter；enabled 时才触发）。nil 取消导出。
func (r *Recorder) SetSink(s Sink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sink = s
}

// Recent 读取最近 n 条记录（跨日合并，按时间倒序）。n<=0 时返回空。
func (r *Recorder) Recent(n int) []Entry {
	if !r.enabled || n <= 0 {
		return nil
	}
	r.mu.Lock()
	day := r.day
	r.mu.Unlock()

	matches, _ := filepath.Glob(filepath.Join(r.dir, "trace-*.jsonl"))
	sort.Strings(matches) // 日期升序，从最新往前读

	var entries []Entry
	for i := len(matches) - 1; i >= 0 && len(entries) < n; i-- {
		data, err := os.ReadFile(matches[i])
		if err != nil {
			continue
		}
		lines := splitLines(data)
		for j := len(lines) - 1; j >= 0 && len(entries) < n; j-- {
			var e Entry
			if json.Unmarshal(lines[j], &e) == nil {
				entries = append(entries, e)
			}
		}
	}
	_ = day
	return entries
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// Close 关闭当前文件并冲刷导出器剩余缓冲。
func (r *Recorder) Close() error {
	r.mu.Lock()
	var err error
	if r.file != nil {
		err = r.file.Close()
		r.file = nil
	}
	sink := r.sink
	r.mu.Unlock()
	if sink != nil {
		_ = sink.Flush()
	}
	return err
}
