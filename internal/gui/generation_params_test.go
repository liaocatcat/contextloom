package gui

// 本次改造 GUI 层测试：model.set 的 off 档白名单与错误信息、
// status.bar 对三项独立长度参数（maxOutputTokens/maxInputTokens/thinkingEnabled）
// 的透传（设置面板保存后状态栏轮询同步）。
//
// 说明：gui 包既有 fakeRuntime（server_test.go）的 SetEffort 为空实现、
// ModelInfo 不含新字段，无法断言"off 转发"与"status.bar 透传"；
// 本文件用嵌入覆盖（effortSpyRuntime）补齐，不修改既有测试文件。

import (
	"strings"
	"sync"
	"testing"
)

// effortSpyRuntime 在 fakeRuntime 之上记录 SetEffort 收到的档位，
// 并返回含三项独立长度参数的 ModelInfo（验证 status.bar 透传）。
type effortSpyRuntime struct {
	*fakeRuntime
	spyMu      sync.Mutex
	lastEffort string
}

func (e *effortSpyRuntime) SetEffort(effort string) {
	e.spyMu.Lock()
	e.lastEffort = effort
	e.spyMu.Unlock()
}

// LastEffort 返回最近一次 SetEffort 收到的档位（并发安全）。
func (e *effortSpyRuntime) LastEffort() string {
	e.spyMu.Lock()
	defer e.spyMu.Unlock()
	return e.lastEffort
}

// ModelInfo 覆盖：返回含三项独立长度参数的状态（fakeRuntime 的原实现
// 只有 backend/model/effort/maxContext）。
func (e *effortSpyRuntime) ModelInfo() map[string]any {
	return map[string]any{
		"backend": "deepseek", "model": "deepseek-v4", "effort": "max", "maxContext": 131072,
		"maxOutputTokens": 65536, "maxInputTokens": 32768, "thinkingEnabled": true,
	}
}

// TestWS_ModelSet_OffEffort：model.set 新增 off 档（关闭思考）与非法档位校验，
// 以及 status.bar 的 model 字段透传三项独立长度参数。
func TestWS_ModelSet_OffEffort(t *testing.T) {
	rt := &effortSpyRuntime{fakeRuntime: &fakeRuntime{mode: "chat"}}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	// off 是本次改造新增的合法档位：应成功并转发到 Runtime.SetEffort
	resp := sendReq(t, conn, "1", "model.set", map[string]any{"effort": "off"})
	if !resp.OK {
		t.Fatalf("model.set {effort:off} 应成功，got error=%v", resp.Error)
	}
	if got := rt.LastEffort(); got != "off" {
		t.Fatalf("SetEffort 应收到 off，got %q", got)
	}

	// 非法档位：报错，且错误信息含合法档位列表（前端提示可用档位）
	resp = sendReq(t, conn, "2", "model.set", map[string]any{"effort": "bogus"})
	if resp.OK {
		t.Fatal("model.set {effort:bogus} 应失败")
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Msg, "off/low/medium/high/xhigh/max") {
		t.Fatalf("错误信息应含合法档位列表，got %+v", resp.Error)
	}

	// 缺 effort：报错
	resp = sendReq(t, conn, "3", "model.set", map[string]any{})
	if resp.OK {
		t.Fatal("缺 effort 应失败")
	}

	// status.bar 的 model 字段透传三项独立长度参数（状态栏 2s 轮询同步）
	resp = sendReq(t, conn, "4", "status.bar", nil)
	if !resp.OK {
		t.Fatalf("status.bar 失败: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("status.bar 结果类型异常: %T", resp.Result)
	}
	modelField, ok := result["model"].(map[string]any)
	if !ok {
		t.Fatalf("status.bar 缺 model 字段: %#v", result)
	}
	// JSON number 经 WS 解码为 float64
	if got := modelField["maxOutputTokens"]; got != float64(65536) {
		t.Fatalf("status.bar.model.maxOutputTokens=%v, want 65536", got)
	}
	if got := modelField["maxInputTokens"]; got != float64(32768) {
		t.Fatalf("status.bar.model.maxInputTokens=%v, want 32768", got)
	}
	if got := modelField["thinkingEnabled"]; got != true {
		t.Fatalf("status.bar.model.thinkingEnabled=%v, want true", got)
	}
}
