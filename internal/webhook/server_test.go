package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServer_StartAndHealth(t *testing.T) {
	s := New("", nil)
	addr, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop(context.Background())
	if !strings.HasPrefix(addr, "http://127.0.0.1:") {
		t.Errorf("地址异常: %s", addr)
	}
	resp, err := http.Get(addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz 应 200, got %d", resp.StatusCode)
	}
}

func TestServer_HookForbidden(t *testing.T) {
	s := New("secret-token-123", nil)
	addr, _ := s.Start()
	defer s.Stop(context.Background())

	resp, err := http.Post(addr+"/hook/wrong-token", "application/json",
		bytes.NewBufferString(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("错误 token 应 403, got %d", resp.StatusCode)
	}
}

func TestServer_HookFullChain(t *testing.T) {
	var calls atomic.Int32
	s := New("tok", func(ctx context.Context, msg Message) (string, error) {
		calls.Add(1)
		if msg.Mode != "goal" {
			t.Errorf("mode 应为 goal, got %s", msg.Mode)
		}
		if msg.Text != "实现登录功能" {
			t.Errorf("text 异常: %s", msg.Text)
		}
		return "已完成: 登录功能实现", nil
	})
	addr, _ := s.Start()
	defer s.Stop(context.Background())

	resp, err := http.Post(addr+"/hook/tok", "application/json",
		bytes.NewBufferString(`{"message":"实现登录功能","mode":"goal"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("应 200, got %d", resp.StatusCode)
	}
	var out struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Result != "已完成: 登录功能实现" {
		t.Errorf("响应异常: %+v", out)
	}
	if calls.Load() != 1 {
		t.Errorf("handler 应调用 1 次, got %d", calls.Load())
	}
}

func TestServer_HookError(t *testing.T) {
	s := New("tok", func(ctx context.Context, msg Message) (string, error) {
		return "", fmt.Errorf("执行失败: boom")
	})
	addr, _ := s.Start()
	defer s.Stop(context.Background())

	resp, err := http.Post(addr+"/hook/tok", "application/json",
		bytes.NewBufferString(`{"message":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Errorf("失败应 500, got %d", resp.StatusCode)
	}
	// 历史应记录错误
	h := s.History()
	if len(h) != 1 || h[0].Err == "" {
		t.Errorf("历史应含错误条目: %+v", h)
	}
}

func TestServer_EmptyMessage(t *testing.T) {
	s := New("tok", nil)
	addr, _ := s.Start()
	defer s.Stop(context.Background())

	resp, err := http.Post(addr+"/hook/tok", "application/json",
		bytes.NewBufferString(`{"message":""}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("空消息应 400, got %d", resp.StatusCode)
	}
}

func TestServer_HistoryRing(t *testing.T) {
	s := New("tok", func(ctx context.Context, msg Message) (string, error) {
		return "ok:" + msg.Text, nil
	})
	addr, _ := s.Start()
	defer s.Stop(context.Background())

	for i := 0; i < 60; i++ {
		body := fmt.Sprintf(`{"message":"msg-%d"}`, i)
		resp, err := http.Post(addr+"/hook/tok", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	h := s.History()
	if len(h) != 50 {
		t.Errorf("历史应封顶 50, got %d", len(h))
	}
	if h[0].Text != "msg-59" {
		t.Errorf("最新应在最前: %s", h[0].Text)
	}
}

func TestServer_HistoryEndpoint(t *testing.T) {
	s := New("tok", func(ctx context.Context, msg Message) (string, error) {
		return "done", nil
	})
	addr, _ := s.Start()
	defer s.Stop(context.Background())

	_, _ = http.Post(addr+"/hook/tok", "application/json", bytes.NewBufferString(`{"message":"a"}`))
	resp, err := http.Get(addr + "/history")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		History []HistoryEntry `json:"history"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.History) != 1 {
		t.Errorf("history 端点异常: %+v", out)
	}
}

func TestRandomToken_Unique(t *testing.T) {
	a, b := randomToken(), randomToken()
	if a == "" || len(a) != 32 || a == b {
		t.Errorf("token 异常: %q vs %q", a, b)
	}
}

func TestServer_Stop(t *testing.T) {
	s := New("tok", nil)
	addr, _ := s.Start()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 停止后连接应失败
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := http.Get(addr + "/healthz")
		if err != nil {
			return // 期望失败
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("停止后服务仍可访问")
}
