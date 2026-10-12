package main

import (
	"bytes"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestEncodePlantUML：编码格式与解压回环测试。
// ~1 格式 = 完整 zlib 流（带 2 字节头+adler32，Java DeflaterOutputStream 兼容）
// → PlantUML 自定义 base64 字母表。正确验证方式是：
//  1. 格式：~1 前缀 + 自定义 base64 合法（可解码为字节）
//  2. 回环：自定义 base64 解码 → zlib 解压 → 必须还原原文
func TestEncodePlantUML(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"basic sequence", "@startuml\nAlice -> Bob: hello\n@enduml"},
		{"short sequence", "@startuml\nA -> B: x\n@enduml"},
		{"chinese text", "@startuml\n用户 -> 系统: 登录请求\n@enduml"},
		{"sequence diagram", "@startuml\nactor User\nparticipant \"First Component\" as A\nparticipant \"Second Component\" as B\nUser -> A: DoWork\nactivate A\nA -> B: CreateRequest\nactivate B\nB --> A: RequestCreated\ndeactivate B\nA --> User: Done\ndeactivate A\n@enduml"},
		{"long class diagram", "@startuml\nclass Base {\n  +String name\n  +int id\n  +void execute()\n}\nclass Child extends Base {\n  -List<String> items\n  +void run()\n}\nBase <|-- Child\nChild *-- Item\n@enduml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := encodePlantUML(c.src)
			if !strings.HasPrefix(got, "~1") {
				t.Fatalf("必须以 ~1 前缀（PlantUML legacy base64 格式），got=%s", got)
			}
			raw, err := plantumlBase64.DecodeString(strings.TrimPrefix(got, "~1"))
			if err != nil {
				t.Fatalf("自定义 base64 解码失败: %v", err)
			}
			zr, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("zlib 头解析失败（必须是完整 zlib 流）: %v", err)
			}
			decoded, err := io.ReadAll(zr)
			zr.Close()
			if err != nil {
				t.Fatalf("zlib 解压失败（输出非法）: %v", err)
			}
			if string(decoded) != c.src {
				t.Fatalf("解压回环不匹配:\n got=%q\nwant=%q", decoded, c.src)
			}
		})
	}
}

// TestPlantumlURL：server 三种配置形态的 URL 拼接。
func TestPlantumlURL(t *testing.T) {
	enc := "~h" + strings.Repeat("ab", 4) // 任意编码片段
	cases := []struct {
		name   string
		server string
		want   string
	}{
		{"根地址追加 /svg/", "https://host", "https://host/svg/" + enc},
		{"标准根 /plantuml 追加 /svg/", "https://www.plantuml.com/plantuml", "https://www.plantuml.com/plantuml/svg/" + enc},
		{"已含 /svg 段不重复", "https://www.plantuml.com/plantuml/svg/", "https://www.plantuml.com/plantuml/svg/" + enc},
		{"已含 /svg 段无尾斜杠", "https://www.plantuml.com/plantuml/svg", "https://www.plantuml.com/plantuml/svg/" + enc},
		{"内网 http + 路径", "http://127.0.0.1:8080/plantuml", "http://127.0.0.1:8080/plantuml/svg/" + enc},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := plantumlURL(c.server, enc)
			if got != c.want {
				t.Fatalf("plantumlURL(%q) = %q, want %q", c.server, got, c.want)
			}
		})
	}
}

// TestPlantumlHandler：mock plantuml-server 全链路。
func TestPlantumlHandler(t *testing.T) {
	var gotPath atomic.Value // 记录 mock server 收到的请求路径
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><text>ok</text></svg>`))
	}))
	defer mock.Close()

	setPreviewCfg := func(server string, enabled bool) {
		previewMu.Lock()
		defer previewMu.Unlock()
		previewCfg.path = "/nonexistent/config.yaml" // 命中缓存，不读真实配置
		previewCfg.mtime = time.Time{}
		previewCfg.server = server
		previewCfg.enabled = enabled
	}

	t.Run("proxy returns svg with cors", func(t *testing.T) {
		setPreviewCfg(mock.URL, true)
		req := httptest.NewRequest(http.MethodGet, "/api/plantuml/svg?src="+urlQuery("A -> B"), nil)
		rec := httptest.NewRecorder()
		plantumlHandler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("期望 200，得到 %d: %s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
			t.Fatalf("Content-Type 应为 image/svg+xml，得到 %s", ct)
		}
		if acao := rec.Header().Get("Access-Control-Allow-Origin"); acao != "*" {
			t.Fatalf("CORS 头应为 *，得到 %q", acao)
		}
		if cc := rec.Header().Get("Cache-Control"); cc == "" {
			t.Fatal("应设置 Cache-Control")
		}
		if !strings.Contains(rec.Body.String(), "<svg") {
			t.Fatalf("应透传 SVG 内容，得到 %s", rec.Body.String())
		}
		p, _ := gotPath.Load().(string)
		if !strings.HasPrefix(p, "/svg/~1") {
			t.Fatalf("mock server 应收到 /svg/~1<base64> 路径，得到 %s", p)
		}
	})

	t.Run("disabled returns 403", func(t *testing.T) {
		setPreviewCfg(mock.URL, false)
		req := httptest.NewRequest(http.MethodGet, "/api/plantuml/svg?src=A%20-%3E%20B", nil)
		rec := httptest.NewRecorder()
		plantumlHandler(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("禁用时应 403，得到 %d", rec.Code)
		}
	})

	t.Run("missing src returns 400", func(t *testing.T) {
		setPreviewCfg(mock.URL, true)
		req := httptest.NewRequest(http.MethodGet, "/api/plantuml/svg", nil)
		rec := httptest.NewRecorder()
		plantumlHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("缺 src 时应 400，得到 %d", rec.Code)
		}
	})

	t.Run("server unreachable returns 502", func(t *testing.T) {
		// 无监听端口：连接必失败
		setPreviewCfg("http://127.0.0.1:1", true)
		req := httptest.NewRequest(http.MethodGet, "/api/plantuml/svg?src=A", nil)
		rec := httptest.NewRecorder()
		plantumlHandler(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("server 不可达时应 502，得到 %d", rec.Code)
		}
	})

	t.Run("non-http server returns 502", func(t *testing.T) {
		setPreviewCfg("ftp://plantuml.example.com", true)
		req := httptest.NewRequest(http.MethodGet, "/api/plantuml/svg?src=A", nil)
		rec := httptest.NewRecorder()
		plantumlHandler(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("非 http(s) server 时应 502，得到 %d", rec.Code)
		}
	})
}

func urlQuery(s string) string {
	// 简化编码：仅替换空格与箭头（测试输入固定，够用）
	r := strings.ReplaceAll(s, " ", "%20")
	return strings.ReplaceAll(r, "->", "-%3E")
}

var _ = io.Discard // 保留 io import（错误响应读取场景）
