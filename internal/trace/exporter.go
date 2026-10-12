// OTLP/HTTP+JSON 导出器（零依赖实现，对标 OpenTelemetry GenAI semconv 生态接入）。
//
// 语义：把 JSONL trace 记录批量导出到任意 OTLP/HTTP 接收端
// （如 OpenTelemetry Collector 的 /v1/logs、Langfuse、Jaeger 等），
// 载荷为 OTLP protobuf JSON 映射（LogsData → resourceLogs/scopeLogs/logRecords）。
//
// 设计：
//   - 非阻塞 Enqueue（channel 满则丢弃并计数，绝不影响 agent 主流程）；
//   - 批量 + 定时双触发（batchSize 满或 interval 到期）；
//   - 失败静默（记录 dropped 计数，不重试不阻塞）；
//   - 零外部依赖（仅 net/http + stdlib），全平台可交叉编译。
package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Exporter OTLP/HTTP+JSON 导出器。
type Exporter struct {
	endpoint  string
	headers   map[string]string
	batchSize int
	interval  time.Duration

	ch       chan Entry
	done     chan struct{}
	flushReq chan chan struct{} // Flush 请求：loop 冲刷缓冲后关闭响应通道
	wg       sync.WaitGroup
	mu       sync.Mutex
	dropped  int64
	sent     int64
	closed   bool
}

// NewExporter 创建导出器。endpoint 为空返回 nil（禁用）。
// 默认 batchSize=32、interval=5s；可在 Start 前用 SetBatch/SetInterval 调整。
func NewExporter(endpoint string, headers map[string]string) *Exporter {
	if endpoint == "" {
		return nil
	}
	return &Exporter{
		endpoint:  endpoint,
		headers:   headers,
		batchSize: 32,
		interval:  5 * time.Second,
		ch:        make(chan Entry, 128),
		flushReq:  make(chan chan struct{}, 1),
	}
}

// SetBatch 设置批量条数（<=0 忽略；须在 Start 前调用）。
func (e *Exporter) SetBatch(n int) {
	if n > 0 {
		e.batchSize = n
	}
}

// SetInterval 设置定时冲刷间隔秒（<=0 忽略；须在 Start 前调用）。
func (e *Exporter) SetInterval(sec int) {
	if sec > 0 {
		e.interval = time.Duration(sec) * time.Second
	}
}

// Start 启动后台冲刷 goroutine（幂等）。
func (e *Exporter) Start() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.done != nil {
		return
	}
	e.done = make(chan struct{})
	e.wg.Add(1)
	go e.loop()
}

// Enqueue 追加一条记录（非阻塞：channel 满则丢弃并计数）。
func (e *Exporter) Enqueue(ent Entry) {
	select {
	case e.ch <- ent:
	default:
		e.mu.Lock()
		e.dropped++
		e.mu.Unlock()
	}
}

// Flush 同步冲刷当前排队与缓冲的全部记录（Close 或收尾时调用）。
// 先 drain channel 中尚未被 loop 取走的条目，再请求 loop 冲刷其内部缓冲。
// loop 已退出（closed）时跳过请求，避免等待永不响应的响应通道。
func (e *Exporter) Flush() error {
	var batch []Entry
drain:
	for {
		select {
		case ent := <-e.ch:
			batch = append(batch, ent)
		default:
			break drain
		}
	}
	if len(batch) > 0 {
		e.post(batch)
	}
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed || e.done == nil {
		return nil // loop 已退出/未启动：无内部缓冲可冲刷
	}
	resp := make(chan struct{})
	select {
	case e.flushReq <- resp:
		select {
		case <-resp:
		case <-time.After(2 * time.Second): // 兜底：不无限等待
		}
	default: // loop 忙（正在 post）→ 其自身会按批次/定时冲刷
	}
	return nil
}

// Close 停止后台 goroutine 并冲刷剩余记录。幂等。
func (e *Exporter) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	if e.done == nil {
		e.mu.Unlock()
		return nil
	}
	// loop 仍在运行：先请求冲刷其内部缓冲，再停止（避免丢数据）
	resp := make(chan struct{})
	select {
	case e.flushReq <- resp:
		select {
		case <-resp:
		case <-time.After(2 * time.Second):
		}
	default: // loop 正在 post：其 done 分支会冲刷缓冲
	}
	close(e.done)
	e.mu.Unlock()
	e.wg.Wait()
	// loop 已退出：drain channel 中尚未被取走的条目
	e.Flush()
	return nil
}

// Counters 返回 (发送条数, 丢弃条数)。
func (e *Exporter) Counters() (sent, dropped int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sent, e.dropped
}

func (e *Exporter) loop() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	var batch []Entry
	for {
		select {
		case <-e.done:
			if len(batch) > 0 {
				e.post(batch) // 退出前冲刷缓冲，不丢数据
			}
			return
		case resp := <-e.flushReq:
			if len(batch) > 0 {
				e.post(batch)
				batch = nil
			}
			close(resp)
		case <-ticker.C:
			if len(batch) > 0 {
				e.post(batch)
				batch = nil
			}
		case ent := <-e.ch:
			batch = append(batch, ent)
			if len(batch) >= e.batchSize {
				e.post(batch)
				batch = nil
			}
		}
	}
}

func (e *Exporter) post(entries []Entry) {
	payload := buildOTLPLogs(entries)
	data, err := json.Marshal(payload)
	if err != nil {
		e.addDropped(len(entries))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(data))
	if err != nil {
		e.addDropped(len(entries))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.addDropped(len(entries))
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		e.addDropped(len(entries))
		return
	}
	e.mu.Lock()
	e.sent += int64(len(entries))
	e.mu.Unlock()
}

func (e *Exporter) addDropped(n int) {
	e.mu.Lock()
	e.dropped += int64(n)
	e.mu.Unlock()
}

// ── OTLP protobuf JSON 映射结构（LogsData）──

type otlpLogs struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

type otlpResourceLogs struct {
	Resource  otlpResource   `json:"resource"`
	ScopeLogs []otlpScopeLog `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeLog struct {
	Scope      otlpScope       `json:"scope"`
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpLogRecord struct {
	TimeUnixNano string         `json:"timeUnixNano"` // int64 字符串（proto3 JSON）
	SeverityText string         `json:"severityText"`
	Body         otlpAnyValue   `json:"body"`
	Attributes   []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"` // int64 字符串（proto3 JSON）
}

func strPtr(s string) *string { return &s }

func intStrPtr(v int64) *string {
	s := strconv.FormatInt(v, 10)
	return &s
}

func kvStr(key, val string) otlpKeyValue {
	return otlpKeyValue{Key: key, Value: otlpAnyValue{StringValue: strPtr(val)}}
}

func kvInt(key string, val int64) otlpKeyValue {
	return otlpKeyValue{Key: key, Value: otlpAnyValue{IntValue: intStrPtr(val)}}
}

// buildOTLPLogs 把 Entry 列表转换为 OTLP LogsData 结构。
func buildOTLPLogs(entries []Entry) otlpLogs {
	records := make([]otlpLogRecord, 0, len(entries))
	for _, ent := range entries {
		ts := ent.TS
		if ts.IsZero() {
			ts = time.Now()
		}
		rec := otlpLogRecord{
			TimeUnixNano: strconv.FormatInt(ts.UnixNano(), 10),
			SeverityText: "INFO",
			Body:         otlpAnyValue{StringValue: strPtr(ent.Event)},
		}
		var attrs []otlpKeyValue
		if ent.Session != "" {
			attrs = append(attrs, kvStr("session", ent.Session))
		}
		if ent.Mode != "" {
			attrs = append(attrs, kvStr("mode", ent.Mode))
		}
		if ent.Round > 0 {
			attrs = append(attrs, kvInt("round", int64(ent.Round)))
		}
		if ent.Tool != "" {
			attrs = append(attrs, kvStr("tool", ent.Tool))
		}
		if ent.Ms > 0 {
			attrs = append(attrs, kvInt("ms", ent.Ms))
		}
		if ent.Tokens > 0 {
			attrs = append(attrs, kvInt("tokens", int64(ent.Tokens)))
		}
		if ent.Err != "" {
			attrs = append(attrs, kvStr("err", ent.Err))
		}
		if ent.Detail != "" {
			attrs = append(attrs, kvStr("detail", ent.Detail))
		}
		rec.Attributes = attrs
		records = append(records, rec)
	}
	return otlpLogs{
		ResourceLogs: []otlpResourceLogs{{
			Resource: otlpResource{Attributes: []otlpKeyValue{
				kvStr("service.name", "contextloom"),
			}},
			ScopeLogs: []otlpScopeLog{{
				Scope:      otlpScope{Name: "contextloom.trace"},
				LogRecords: records,
			}},
		}},
	}
}

// String 便于日志展示（实现 fmt.Stringer）。
func (e *Exporter) String() string {
	sent, dropped := e.Counters()
	return fmt.Sprintf("otlp-exporter(endpoint=%s sent=%d dropped=%d)", e.endpoint, sent, dropped)
}
