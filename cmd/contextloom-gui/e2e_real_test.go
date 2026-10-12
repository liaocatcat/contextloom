package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestE2E_PlantUMLRealServer：真实请求用户配置的 plantuml-server，
// 验证 ~1 编码 + /svg 前缀拼接（用户配置 https://www.plantuml.com/plantuml/svg/）
// 全链路可用。网络测试：服务器不可达时跳过（不阻塞离线单测）。
func TestE2E_PlantUMLRealServer(t *testing.T) {
	if os.Getenv("PLANTUML_E2E") == "" {
		t.Skip("设置 PLANTUML_E2E=1 运行真实服务器 E2E")
	}
	// 从环境变量读取一份含 @startuml 块的 Markdown 作为测试夹具。
	fixture := os.Getenv("PLANTUML_FIXTURE")
	if fixture == "" {
		t.Skip("设置 PLANTUML_FIXTURE 指向含 @startuml 块的 Markdown 文件")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Skipf("夹具文件不可读: %v", err)
	}
	md := string(data)
	start := strings.Index(md, "@startuml")
	end := strings.Index(md[start:], "@enduml")
	if start < 0 || end < 0 {
		t.Fatal("未找到 @startuml 块")
	}
	block := md[start : start+end+len("@enduml")]

	server := "https://www.plantuml.com/plantuml/svg/"
	u := plantumlURL(server, encodePlantUML(block))
	t.Logf("请求 URL 长度: %d", len(u))
	cli := &http.Client{Timeout: 20 * time.Second}
	resp, err := cli.Get(u)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d, body 前 100: %s", resp.StatusCode, body[:100])
	}
	if !strings.HasPrefix(strings.TrimSpace(string(body)), "<svg") {
		t.Fatalf("非 SVG 响应: %s", body[:100])
	}
	if strings.Contains(string(body), "X-PlantUML-Diagram-Error") ||
		strings.Contains(string(body), "Syntax Error") {
		t.Fatal("服务器返回错误图（Syntax Error）")
	}
	t.Logf("✅ E2E 成功: HTTP 200, SVG %d 字节", len(body))
}
