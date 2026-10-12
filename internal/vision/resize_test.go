package vision

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// genImage 生成 nw×nh 的渐变色测试图（PNG/JPEG 编码）。
func genImage(t *testing.T, nw, nh int, enc string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	switch enc {
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("PNG 编码失败: %v", err)
		}
	case "jpeg":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			t.Fatalf("JPEG 编码失败: %v", err)
		}
	default:
		t.Fatalf("未知编码 %q", enc)
	}
	return buf.Bytes()
}

// TestCompressImage_SmallKeeps 小图（<200KB）不压缩，原样返回。
func TestCompressImage_SmallKeeps(t *testing.T) {
	data := genImage(t, 100, 100, "png") // 渐变 PNG 很小
	out, mime, changed := CompressImage(data, "image/png")
	if changed {
		t.Errorf("小图不应被压缩")
	}
	if !bytes.Equal(out, data) || mime != "image/png" {
		t.Errorf("小图应原样返回")
	}
}

// TestCompressImage_LargeJPEG 大 JPEG 缩放重编码：体积明显变小、MIME 保持。
func TestCompressImage_LargeJPEG(t *testing.T) {
	data := genImage(t, 2400, 1600, "jpeg") // ~几 MB
	if len(data) < compressMinBytes {
		t.Skipf("测试图 %d 字节 < 压缩阈值，改用更大图", len(data))
	}
	out, mime, changed := CompressImage(data, "image/jpeg")
	if !changed {
		t.Fatalf("大 JPEG 应被压缩")
	}
	if mime != "image/jpeg" {
		t.Errorf("JPEG 压缩后 MIME 应保持 image/jpeg，got %q", mime)
	}
	if len(out) >= len(data) {
		t.Errorf("压缩后应小于原图: %d >= %d", len(out), len(data))
	}
	// 解码验证尺寸 ≤ 800 且能正常解码。
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("压缩结果解码失败: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("压缩结果应为 jpeg，got %q", format)
	}
	if b := img.Bounds(); b.Dx() > DeepSeekMaxDim || b.Dy() > DeepSeekMaxDim {
		t.Errorf("压缩后尺寸 %dx%d 应 ≤ %d", b.Dx(), b.Dy(), DeepSeekMaxDim)
	}
}

// TestCompressImage_PNGKeepsPNG 大 PNG 压缩后仍是 PNG（保留透明/截图清晰度），
// 且尺寸合规。
func TestCompressImage_PNGKeepsPNG(t *testing.T) {
	// 构造一个大 PNG：大尺寸伪随机噪声 → PNG 压缩率低，体积必然超 200KB。
	dim := 2600
	img := image.NewRGBA(image.Rect(0, 0, dim, dim))
	// 用可复现的伪随机（非加密 HKDF 风格）：x,y 线性同余混合，避免纯渐变
	// 被 PNG 预测器压得太小。
	state := uint32(12345)
	for y := 0; y < dim; y++ {
		for x := 0; x < dim; x++ {
			state = state*1103515245 + 12345
			img.Set(x, y, color.RGBA{R: uint8(state >> 24), G: uint8(state >> 16), B: uint8(state >> 8), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("PNG 编码失败: %v", err)
	}
	if buf.Len() < compressMinBytes {
		t.Skipf("PNG 测试图 %d 字节未超阈值（请调大 dim）", buf.Len())
	}
	t.Logf("原图 %d 字节 → 期望压缩", buf.Len())
	out, mime, changed := CompressImage(buf.Bytes(), "image/png")
	if !changed {
		t.Fatalf("大 PNG 应被压缩")
	}
	if mime != "image/png" {
		t.Errorf("PNG 压缩后 MIME 应保持 image/png，got %q", mime)
	}
	img2, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("压缩结果解码失败: %v", err)
	}
	if b := img2.Bounds(); b.Dx() > DeepSeekMaxDim || b.Dy() > DeepSeekMaxDim {
		t.Errorf("压缩后尺寸 %dx%d 应 ≤ %d", b.Dx(), b.Dy(), DeepSeekMaxDim)
	}
}

// TestCompressImage_InvalidKeeps 伪造/不可解码字节原样返回（不误伤）。
func TestCompressImage_InvalidKeeps(t *testing.T) {
	data := bytes.Repeat([]byte{0xFF}, compressMinBytes+1)
	out, mime, changed := CompressImage(data, "image/png")
	if changed {
		t.Errorf("伪造图片不应被压缩")
	}
	if !bytes.Equal(out, data) || mime != "image/png" {
		t.Errorf("伪造图片应原样返回")
	}
}

// TestCompressImage_NoGainKeeps 压缩后体积不小于原图 → 保留原图（不劣化）。
// 用 800×800 随机噪声 PNG：超过 200KB 阈值，但最长边恰好 ≤800 不缩放，
// 无损 PNG 重编码输出与原图字节相同 → out.Len() >= len(data) → 触发"无增益
// 回退"分支（changed=false 且原样返回）。
func TestCompressImage_NoGainKeeps(t *testing.T) {
	dim := 800
	img := image.NewRGBA(image.Rect(0, 0, dim, dim))
	state := uint32(987654)
	for y := 0; y < dim; y++ {
		for x := 0; x < dim; x++ {
			state = state*1103515245 + 12345
			img.Set(x, y, color.RGBA{R: uint8(state >> 24), G: uint8(state >> 16), B: uint8(state >> 8), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("PNG 编码失败: %v", err)
	}
	if buf.Len() < compressMinBytes {
		t.Skipf("噪声图 %d 字节未超阈值（请调大 dim）", buf.Len())
	}
	out, mime, changed := CompressImage(buf.Bytes(), "image/png")
	if changed {
		t.Errorf("无增益时不应标记已压缩（应回退原图）")
	}
	if !bytes.Equal(out, buf.Bytes()) || mime != "image/png" {
		t.Errorf("无增益时应原样返回原图")
	}
}
