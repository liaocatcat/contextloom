package vision

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// fakePNG 生成一个带 PNG 魔数的伪造图片字节（不校验图片有效性）。
func fakePNG() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte("fake-image-content")...)
}

// writeTemp 在 dir 下写入 name 文件，返回其绝对路径。
func writeTemp(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatalf("写测试文件 %s 失败: %v", p, err)
	}
	return p
}

// mustDetect 断言 DetectImages 无错误。
func mustDetect(t *testing.T, text, baseDir string) []Image {
	t.Helper()
	imgs, err := DetectImages(text, baseDir)
	if err != nil {
		t.Fatalf("DetectImages(%q) 返回错误: %v", text, err)
	}
	return imgs
}

// ── HasImagePath / MIMEForPath ──

func TestHasImagePath_Basics(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"看 ./a.png", true},
		{"\"my pic.JPG\" 和 'x.webp'", true},
		{"路径 /tmp/shot.bmp 里有什么", true},
		{"后缀 .gif 也能识别", true},
		{"没有图片，只有文本", false},
		{"a.png 后跟文字 (a.png)", true},
		{"note.txt 和 readme.md", false},
	}
	for _, c := range cases {
		if got := HasImagePath(c.text); got != c.want {
			t.Errorf("HasImagePath(%q) = %v，期望 %v", c.text, got, c.want)
		}
	}
}

func TestMIMEForPath(t *testing.T) {
	cases := map[string]string{
		"a.png":  "image/png",
		"B.JPG":  "image/jpeg",
		"c.jpeg": "image/jpeg",
		"d.gif":  "image/gif",
		"e.webp": "image/webp",
		"f.bmp":  "image/bmp",
	}
	for path, want := range cases {
		mime, ok := MIMEForPath(path)
		if !ok || mime != want {
			t.Errorf("MIMEForPath(%q) = (%q,%v)，期望 (%q,true)", path, mime, ok, want)
		}
	}
	if _, ok := MIMEForPath("a.txt"); ok {
		t.Errorf("MIMEForPath(a.txt) 不应命中")
	}
	// 扩展名匹配必须完整（.pngx 不是 .png）
	if _, ok := MIMEForPath("a.pngx"); ok {
		t.Errorf("MIMEForPath(a.pngx) 不应命中（前缀误判）")
	}
}

// ── DetectImages ──

func TestDetectImages_AbsPath(t *testing.T) {
	content := fakePNG()
	p := writeTemp(t, t.TempDir(), "a.png", content)

	imgs := mustDetect(t, "看这张图 "+p+" 谢谢", "")
	if len(imgs) != 1 {
		t.Fatalf("期望收集 1 张图片，实际 %d", len(imgs))
	}
	img := imgs[0]
	if img.MIME != "image/png" {
		t.Errorf("MIME = %q，期望 %q", img.MIME, "image/png")
	}
	if img.Path != filepath.Clean(p) {
		t.Errorf("Path = %q，期望 %q", img.Path, filepath.Clean(p))
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(img.DataURI, prefix) {
		t.Errorf("DataURI = %q，缺少前缀 %q", img.DataURI, prefix)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(img.DataURI, prefix))
	if err != nil {
		t.Fatalf("DataURI base64 解码失败: %v", err)
	}
	if string(decoded) != string(content) {
		t.Errorf("解码内容与文件不一致")
	}
	if img.Size != int64(len(content)) {
		t.Errorf("Size = %d，期望 %d", img.Size, len(content))
	}
}

func TestDetectImages_RelativePath(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "pic.png", fakePNG())

	imgs := mustDetect(t, "看图 pic.png", dir)
	if len(imgs) != 1 || imgs[0].Path != filepath.Join(dir, "pic.png") {
		t.Fatalf("相对路径命中失败: %+v", imgs)
	}
}

func TestDetectImages_QuotedPathsWithSpaces(t *testing.T) {
	dir := t.TempDir()
	p1 := writeTemp(t, dir, "my pic.PNG", fakePNG())     // 双引号 + 大写扩展名
	p2 := writeTemp(t, dir, "an other.jpg", fakePNG())   // 单引号
	p3 := writeTemp(t, dir, "third one.webp", fakePNG()) // 反引号

	text := `看这些 "my pic.PNG" 和 'an other.jpg' 还有 ` + "`third one.webp`" + `。`
	imgs := mustDetect(t, text, dir)
	if len(imgs) != 3 {
		t.Fatalf("期望 3 张，实际 %d: %+v", len(imgs), imgs)
	}
	wantOrder := []string{filepath.Clean(p1), filepath.Clean(p2), filepath.Clean(p3)}
	for i, img := range imgs {
		if img.Path != wantOrder[i] {
			t.Errorf("第 %d 张 Path = %q，期望 %q（文本顺序）", i, img.Path, wantOrder[i])
		}
	}
}

func TestDetectImages_QuotedTrailingPunct(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.png", fakePNG())
	// 引号内尾部逗号：路径必须剥除标点后仍命中
	imgs := mustDetect(t, `看图 "a.png,"`, dir)
	if len(imgs) != 1 || imgs[0].Path != filepath.Clean(p) {
		t.Fatalf("引号内尾标点剥除失败: %+v", imgs)
	}
}

func TestDetectImages_FullWidthQuotes(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.png", fakePNG())
	imgs := mustDetect(t, "看图 “a.png”", dir)
	if len(imgs) != 1 || imgs[0].Path != filepath.Clean(p) {
		t.Fatalf("全角引号剥除失败: %+v", imgs)
	}
}

func TestDetectImages_MissingAndNonImage(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "note.txt", []byte("hello"))
	imgs := mustDetect(t, "看 ./nope.png 和 note.txt", dir)
	if len(imgs) != 0 {
		t.Fatalf("不存在路径/非图片后缀应跳过，实际 %+v", imgs)
	}
}

func TestDetectImages_Dedupe(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "a.png", fakePNG())
	imgs := mustDetect(t, "看 a.png 再看 ./a.png 最后 a.png", dir)
	if len(imgs) != 1 {
		t.Fatalf("同一路径应去重，实际 %d 张", len(imgs))
	}
	if imgs[0].Path != filepath.Clean(p) {
		t.Errorf("Path = %q，期望 %q", imgs[0].Path, filepath.Clean(p))
	}
}

func TestDetectImages_OversizeError(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "big.png", []byte("xx"))
	old := MaxImageBytes
	MaxImageBytes = 1 // 调小上限触发超限（文件 2 字节 > 上限 1）
	defer func() { MaxImageBytes = old }()

	_, err := DetectImages("看 big.png", dir)
	if err == nil || !strings.Contains(err.Error(), "超过上限") {
		t.Fatalf("超限应报错（含路径与上限），实际 %v", err)
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("错误信息应含路径 %s: %v", p, err)
	}
}

func TestDetectImages_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无用户主目录: %v", err)
	}
	dir := t.TempDir()
	_ = dir
	p := filepath.Join(home, "contextloom-vision-test.png")
	if err := os.WriteFile(p, fakePNG(), 0o644); err != nil {
		t.Skipf("无法写入主目录: %v", err)
	}
	defer os.Remove(p)

	imgs := mustDetect(t, "看 ~/contextloom-vision-test.png", "")
	if len(imgs) != 1 || imgs[0].Path != filepath.Clean(p) {
		t.Fatalf("~ 展开失败: %+v，期望 %s", imgs, p)
	}
}

// ── BuildUserMessage / BuildImageParts ──

func TestBuildImageParts(t *testing.T) {
	imgs := []Image{
		{Path: "/a.png", MIME: "image/png", DataURI: "data:image/png;base64,AA"},
		{Path: "/b.jpg", MIME: "image/jpeg", DataURI: "data:image/jpeg;base64,BB"},
	}
	parts := BuildImageParts(imgs)
	if len(parts) != 2 {
		t.Fatalf("期望 2 个 part，实际 %d", len(parts))
	}
	for i, p := range parts {
		if p.Type != "image_url" || p.ImageURL == nil || p.ImageURL.URL != imgs[i].DataURI {
			t.Errorf("第 %d 个 part 结构错误: %+v", i, p)
		}
	}
}

func TestBuildUserMessage(t *testing.T) {
	// 无图：Content 退化为纯字符串
	msg := BuildUserMessage("你好", nil)
	if s, ok := msg.Content.(string); !ok || s != "你好" {
		t.Errorf("无图时 Content = %#v，期望纯文本", msg.Content)
	}

	// 有图：先 text 后 image_url，JSON 结构符合 OpenAI 兼容格式
	imgs := []Image{{Path: "/a.png", MIME: "image/png", DataURI: "data:image/png;base64,QUJD"}}
	msg = BuildUserMessage("看图", imgs)
	parts, ok := msg.Content.([]types.ContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("有图时 Content = %#v，期望 2 个 part", msg.Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "看图" {
		t.Errorf("第 1 个 part 应为 text: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,QUJD" {
		t.Errorf("第 2 个 part 应为 image_url: %+v", parts[1])
	}
	// JSON 序列化形状验证（OpenAI 兼容：type/image_url 嵌套 url）
	b, err := json.Marshal(msg.Content)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	const wantShape = `[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]`
	if string(b) != wantShape {
		t.Errorf("JSON = %s，期望 %s", b, wantShape)
	}
}

// ── EventText / MessageFromEventText ──

func TestEventText(t *testing.T) {
	imgs := []Image{
		{Path: "/a.png", MIME: "image/png", DataURI: "data:image/png;base64,AA"},
		{Path: "/b.jpg", MIME: "image/jpeg", DataURI: "data:image/jpeg;base64,BB"},
	}
	want := "看这两张\n[图片: /a.png]\n[图片: /b.jpg]"
	if got := EventText("看这两张", imgs); got != want {
		t.Errorf("EventText = %q，期望 %q", got, want)
	}
	if got := EventText("纯文本", nil); got != "纯文本" {
		t.Errorf("无图时 EventText = %q，期望原文", got)
	}
	if got := EventText("", nil); got != "" {
		t.Errorf("空文本 EventText = %q，期望空串", got)
	}
}

func TestMessageFromEventText_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "photo.png", fakePNG())
	text := EventText("看这张", []Image{{Path: p, MIME: "image/png", DataURI: "data:image/png;base64,AA", Size: 1}})

	msg := MessageFromEventText(text)
	parts, ok := msg.Content.([]types.ContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("恢复后 Content = %#v，期望 text+image 两个 part", msg.Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "看这张" {
		t.Errorf("文本 part 恢复错误: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("图片 part 恢复错误: %+v", parts[1])
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("恢复的 DataURI 前缀错误: %q", parts[1].ImageURL.URL[:min(30, len(parts[1].ImageURL.URL))])
	}
}

func TestMessageFromEventText_MissingFile(t *testing.T) {
	text := "看这张\n[图片: /不存在的路径/gone.png]"
	msg := MessageFromEventText(text)
	if msg.Role != "user" {
		t.Errorf("Role = %q，期望 user", msg.Role)
	}
	s, ok := msg.Content.(string)
	if !ok {
		t.Fatalf("文件丢失时应退化纯文本，实际 %#v", msg.Content)
	}
	if !strings.Contains(s, "[图片已失效: /不存在的路径/gone.png]") {
		t.Errorf("缺少失效提示: %q", s)
	}
	if !strings.Contains(s, "看这张") {
		t.Errorf("原文丢失: %q", s)
	}
}

func TestMessageFromEventText_PlainText(t *testing.T) {
	msg := MessageFromEventText("普通消息")
	if s, ok := msg.Content.(string); !ok || s != "普通消息" {
		t.Errorf("无标记文本应原样恢复: %#v", msg.Content)
	}
}

// 用户原文恰好含 "[图片: xxx]" 但扩展名非图片 → 必须原样保留为文本，
// 不能被误当图片标记行移除或改写。
func TestMessageFromEventText_NonImageMarkerKeptAsText(t *testing.T) {
	text := "参考 [图片: foo.txt] 这份说明"
	msg := MessageFromEventText(text)
	s, ok := msg.Content.(string)
	if !ok {
		t.Fatalf("非图片标记不应触发多模态，实际 %#v", msg.Content)
	}
	if s != text {
		t.Errorf("原文被改动: %q，期望 %q", s, text)
	}
}

// ── tokenize 边界 ──

func TestTokenize_EdgeCases(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"a.png", []string{"a.png"}},
		{"(./a.png)", []string{"./a.png"}},
		{"看 ./a.png, 和 ./b.jpg。", []string{"看", "./a.png", "和", "./b.jpg"}},
		{"中文。路径/x.png！", []string{"中文。路径/x.png"}},
		{`"空引号" 后 "real.png"`, []string{"空引号", "后", "real.png"}},
		{"未闭合 'a.png", []string{"未闭合", "a.png"}},
		{"", nil},
	}
	for _, c := range cases {
		got := tokenize(c.text)
		if len(got) != len(c.want) {
			t.Errorf("tokenize(%q) = %v，期望 %v", c.text, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("tokenize(%q)[%d] = %q，期望 %q", c.text, i, got[i], c.want[i])
			}
		}
	}
}

// TestDetectImages_LargeCompressed 端到端验证接入点：DetectImages 读大图时
// 经 loadImageFile → CompressImage 压缩——data URI 解码后尺寸 ≤800、MIME 正确、
// 体积显著小于原文件（本地压缩真正生效，非只改预算估算）。
func TestDetectImages_LargeCompressed(t *testing.T) {
	dir := t.TempDir()
	// 2400×1600 噪声 JPEG ≈ 几 MB（远超压缩阈值）。
	dimW, dimH := 2400, 1600
	img := image.NewRGBA(image.Rect(0, 0, dimW, dimH))
	state := uint32(42)
	for y := 0; y < dimH; y++ {
		for x := 0; x < dimW; x++ {
			state = state*1103515245 + 12345
			img.Set(x, y, color.RGBA{R: uint8(state >> 24), G: uint8(state >> 16), B: uint8(state >> 8), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("JPEG 编码失败: %v", err)
	}
	rawSize := buf.Len()
	if rawSize < compressMinBytes {
		t.Skipf("测试图 %d 字节未超压缩阈值", rawSize)
	}
	path := filepath.Join(dir, "big.jpg")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	imgs, err := DetectImages(path, dir)
	if err != nil {
		t.Fatalf("DetectImages 错误: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("应检出 1 张图，got %d", len(imgs))
	}
	got := imgs[0]
	if got.MIME != "image/jpeg" {
		t.Errorf("MIME 应为 image/jpeg，got %q", got.MIME)
	}
	// data URI 解码后应远小于原文件（压缩生效）。
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got.DataURI, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatalf("data URI 解码失败: %v", err)
	}
	if len(payload) >= rawSize {
		t.Errorf("压缩后 %d 应显著小于原文件 %d", len(payload), rawSize)
	}
	// 解码验证尺寸合规。
	dec, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("压缩图解码失败: %v", err)
	}
	if b := dec.Bounds(); b.Dx() > DeepSeekMaxDim || b.Dy() > DeepSeekMaxDim {
		t.Errorf("压缩后尺寸 %dx%d 应 ≤ %d", b.Dx(), b.Dy(), DeepSeekMaxDim)
	}
	t.Logf("原 %d 字节 → 压缩 %d 字节（%.1f%%）", rawSize, len(payload), 100*float64(len(payload))/float64(rawSize))
}
