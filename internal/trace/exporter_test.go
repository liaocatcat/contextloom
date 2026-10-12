package trace

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// captureServer 捕获 OTLP POST 请求的测试服务器。
type captureServer struct {
	srv    *httptest.Server
	mu     atomic.Int64
	bodies chan []byte
}

func newCaptureServer(t *testing.T) *captureServer {
	c := &captureServer{bodies: make(chan []byte, 16)}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(415)
			return
		}
		if k := r.Header.Get("X-Test-Key"); k != "" && k != "secret" {
			w.WriteHeader(403)
			return
		}
		data, _ := io.ReadAll(r.Body)
		c.mu.Add(1)
		select {
		case c.bodies <- data:
		default:
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureServer) count() int64 { return c.mu.Load() }

// waitBody 等待收到一个请求体（超时 2s）。
func (c *captureServer) waitBody(t *testing.T) []byte {
	t.Helper()
	select {
	case b := <-c.bodies:
		return b
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 OTLP POST")
		return nil
	}
}

func TestExporter_EmptyEndpoint_ReturnsNil(t *testing.T) {
	if e := NewExporter("", nil); e != nil {
		t.Fatal("空 endpoint 应返回 nil")
	}
}

func TestExporter_PostOTLPJSON(t *testing.T) {
	c := newCaptureServer(t)
	exp := NewExporter(c.srv.URL+"/v1/logs", map[string]string{"X-Test-Key": "secret"})
	if exp == nil {
		t.Fatal("NewExporter 返回 nil")
	}
	exp.SetBatch(2)
	exp.Start()

	exp.Enqueue(Entry{Event: "agent_round", Session: "sess1", Mode: "task", Round: 3, Tool: "read_file", Ms: 123, Tokens: 456})
	exp.Enqueue(Entry{Event: "tool_call", Mode: "goal", Round: 1, Tool: "exec_command", Ms: 7})

	body := c.waitBody(t)
	var payload struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue *string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeLogs []struct {
				Scope struct {
					Name string `json:"name"`
				} `json:"scope"`
				LogRecords []struct {
					TimeUnixNano string `json:"timeUnixNano"`
					SeverityText string `json:"severityText"`
					Body         struct {
						StringValue *string `json:"stringValue"`
					} `json:"body"`
					Attributes []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue *string `json:"stringValue"`
							IntValue    *string `json:"intValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("OTLP JSON 解析失败: %v\n%s", err, body)
	}
	if len(payload.ResourceLogs) != 1 {
		t.Fatalf("resourceLogs 数 = %d", len(payload.ResourceLogs))
	}
	rl := payload.ResourceLogs[0]
	if len(rl.Resource.Attributes) == 0 || *rl.Resource.Attributes[0].Value.StringValue != "contextloom" {
		t.Fatalf("service.name 缺失: %+v", rl.Resource.Attributes)
	}
	if len(rl.ScopeLogs) != 1 || rl.ScopeLogs[0].Scope.Name != "contextloom.trace" {
		t.Fatalf("scope 错误: %+v", rl.ScopeLogs)
	}
	records := rl.ScopeLogs[0].LogRecords
	if len(records) != 2 {
		t.Fatalf("logRecords 数 = %d, want 2", len(records))
	}
	// 第一条：事件/属性齐全
	rec := records[0]
	if rec.TimeUnixNano == "" || rec.SeverityText != "INFO" {
		t.Fatalf("time/severity 错误: %+v", rec)
	}
	if rec.Body.StringValue == nil || *rec.Body.StringValue != "agent_round" {
		t.Fatalf("body 错误: %+v", rec.Body)
	}
	attrs := map[string]*string{}
	attrints := map[string]*string{}
	for _, a := range rec.Attributes {
		if a.Value.StringValue != nil {
			attrs[a.Key] = a.Value.StringValue
		}
		if a.Value.IntValue != nil {
			attrints[a.Key] = a.Value.IntValue
		}
	}
	if *attrs["session"] != "sess1" || *attrs["mode"] != "task" || *attrs["tool"] != "read_file" {
		t.Fatalf("属性错误: %+v", attrs)
	}
	if *attrints["round"] != "3" || *attrints["ms"] != "123" || *attrints["tokens"] != "456" {
		t.Fatalf("int 属性应为字符串: %+v", attrints)
	}
	// 第二条：空字段省略
	rec2 := records[1]
	attrs2 := map[string]bool{}
	for _, a := range rec2.Attributes {
		attrs2[a.Key] = true
	}
	if attrs2["session"] {
		t.Fatal("空 session 不应出现")
	}
	// 显式关闭：wg.Wait 保证 loop goroutine 与 post() 全部完成（含 sent 计数更新）。
	// 修复竞态：waitBody 在服务器【收到请求体】即返回，而 post() 的 sent 累加在
	// 【收到 HTTP 响应】后才执行——不关闭则 Counters() 可能读到 0/0（flaky）。
	if err := exp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	sent, dropped := exp.Counters()
	if sent != 2 || dropped != 0 {
		t.Fatalf("counters = %d/%d", sent, dropped)
	}
}

func TestExporter_TimerFlush(t *testing.T) {
	c := newCaptureServer(t)
	exp := NewExporter(c.srv.URL+"/v1/logs", nil)
	exp.SetInterval(1) // 1s 定时
	exp.Start()
	defer exp.Close()

	exp.Enqueue(Entry{Event: "error", Err: "boom"})
	body := c.waitBody(t)
	var payload struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []json.RawMessage `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ResourceLogs[0].ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("定时冲刷应含 1 条记录")
	}
}

func TestExporter_CloseFlushesRemaining(t *testing.T) {
	c := newCaptureServer(t)
	exp := NewExporter(c.srv.URL+"/v1/logs", nil)
	exp.SetInterval(3600) // 定时极长，确保只有 Close 触发
	exp.Start()
	exp.Enqueue(Entry{Event: "goal_round", Detail: "x"})
	exp.Close() // Close 内 flush
	body := c.waitBody(t)
	if len(body) == 0 {
		t.Fatal("Close 后应发出剩余记录")
	}
}

func TestExporter_NonBlockingWhenFull(t *testing.T) {
	c := newCaptureServer(t)
	exp := NewExporter(c.srv.URL+"/v1/logs", nil)
	exp.SetInterval(3600)
	exp.Start()
	defer exp.Close()

	// 填满 channel（cap=128）
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			exp.Enqueue(Entry{Event: "flood"})
		}
	}()
	select {
	case <-done:
		// 未阻塞，通过
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue 阻塞了主流程")
	}
	_, dropped := exp.Counters()
	if dropped <= 0 {
		t.Fatal("应发生丢弃")
	}
}

func TestExporter_StartIdempotent(t *testing.T) {
	exp := NewExporter("http://127.0.0.1:1/v1/logs", nil)
	exp.Start()
	exp.Start() // 第二次不 panic
	exp.Close()
}

func TestRecorder_SetSink_TriggersOnRecord(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var calls int64
	r.SetSink(sinkFunc(func(e Entry) { atomic.AddInt64(&calls, 1) }))
	r.Record(Entry{Event: "agent_round"})
	r.Record(Entry{Event: "tool_call"})
	if got := atomic.LoadInt64(&calls); got != 2 {
		t.Fatalf("sink 调用 %d 次, want 2", got)
	}
}

func TestRecorder_SetSink_DisabledNoTrigger(t *testing.T) {
	r, err := New(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var calls int64
	r.SetSink(sinkFunc(func(e Entry) { atomic.AddInt64(&calls, 1) }))
	r.Record(Entry{Event: "x"})
	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("disabled 时 sink 不应触发, got %d", got)
	}
}

func TestRecorder_SetSink_NilCancel(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var calls int64
	r.SetSink(sinkFunc(func(e Entry) { atomic.AddInt64(&calls, 1) }))
	r.SetSink(nil)
	r.Record(Entry{Event: "x"})
	if got := atomic.LoadInt64(&calls); got != 0 {
		t.Fatalf("nil sink 不应触发, got %d", got)
	}
}

// TestRecorder_CloseFlushesExporter：Recorder.Close 通过 flusher 接口冲刷 Exporter。
func TestRecorder_CloseFlushesExporter(t *testing.T) {
	c := newCaptureServer(t)
	exp := NewExporter(c.srv.URL+"/v1/logs", nil)
	exp.SetInterval(3600)
	exp.Start()
	defer exp.Close()

	dir := t.TempDir()
	r, err := New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	r.SetSink(exp)
	r.Record(Entry{Event: "agent_round"})
	// 不调 exp.Close，只调 r.Close → 应通过 flusher 接口冲刷
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	c.waitBody(t)
}

// sinkFunc 把函数适配为 Sink 接口（测试用）。
type sinkFunc func(Entry)

func (f sinkFunc) Enqueue(e Entry) { f(e) }
func (f sinkFunc) Flush() error    { return nil }
