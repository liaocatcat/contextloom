package vision

// 内置图片压缩工具：在图片进入模型前按 DeepSeek 官方视觉文档的缩放规则
// （~800×800 总像素、每张 <384 token）做本地压缩，把"原样 base64 直传"改为
// "先压缩再编码"，从源头降低请求体与 token 成本。
//
// 背景：本项目未来会接入非 DeepSeek 接口，不同服务商对图片大小/格式的要求
// 各异；本地先行压缩到官方标准尺寸，既能满足 DeepSeek（384 token/张封顶），
// 也能让其他接口收到统一规格的小图。本文件不依赖网络与配置，纯函数库。
//
// 依赖：golang.org/x/image（纯 Go 实现，无 cgo）——保证 Windows 交叉编译
// （CGO_ENABLED=0）可用。webp/bmp 解码来自 x/image，标准库无此能力。

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/bmp"
	"golang.org/x/image/webp"
)

// DeepSeekMaxDim 官方缩放目标：图片最长边缩放至 ~800 像素（总像素 ~800×800）。
const DeepSeekMaxDim = 800

// JPEGQuality 重编码 JPEG 质量（85 是体积/质量均衡点，肉眼几乎无差）。
const JPEGQuality = 85

// compressMinBytes 小于该字节数的图片不压缩（小图原样直传，避免无谓 CPU 与
// 质量损失；官方 384 token/张封顶对小图同样适用，压缩没有收益）。
const compressMinBytes = 200 * 1024 // 200KB

// CompressImage 按 DeepSeek 官方缩放标准压缩图片字节：
//
//   - 输入 < compressMinBytes → 原样返回（不压缩）；
//   - GIF 动图 → 原样返回（压缩会丢帧，动图语义不可接受）；
//   - 解码失败（含测试用伪造字节）→ 原样返回（不误伤）；
//   - 最长边 > 800 → 等比缩放到 800；
//   - 重编码：原 JPEG → JPEG q85；webp/bmp → JPEG q85（照片场景）；
//     PNG → PNG（保留透明/截图清晰度）；
//   - 压缩后体积不小于原图 → 放弃压缩返回原图（不劣化兜底）。
//
// 返回 (压缩后字节, 输出 MIME, 是否发生了压缩)。MIME 可能变化
// （webp/bmp → image/jpeg），调用方需用新 MIME 构造 data URI 与落盘扩展名。
func CompressImage(data []byte, mime string) ([]byte, string, bool) {
	if len(data) < compressMinBytes {
		return data, mime, false
	}
	img, format, err := decodeAny(data)
	if err != nil {
		return data, mime, false
	}
	if format == "gif" {
		return data, mime, false // 动图保留
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	resized := img
	if w > DeepSeekMaxDim || h > DeepSeekMaxDim {
		// 等比缩放，最长边 = DeepSeekMaxDim。
		scale := float64(DeepSeekMaxDim) / float64(max(w, h))
		nw := int(float64(w) * scale)
		if nw < 1 {
			nw = 1
		}
		nh := int(float64(h) * scale)
		if nh < 1 {
			nh = 1
		}
		resized = scaleImage(img, nw, nh)
	}

	var out bytes.Buffer
	outMime := mime
	switch {
	case mime == "image/jpeg":
		outMime = "image/jpeg"
		err = jpeg.Encode(&out, resized, &jpeg.Options{Quality: JPEGQuality})
	case mime == "image/png":
		outMime = "image/png"
		err = png.Encode(&out, resized)
	default:
		// webp/bmp 无标准编码器（x/image 只提供 webp 解码）→ 统一转 JPEG。
		outMime = "image/jpeg"
		err = jpeg.Encode(&out, resized, &jpeg.Options{Quality: JPEGQuality})
	}
	if err != nil {
		return data, mime, false
	}
	if out.Len() >= len(data) {
		// 压缩没有收益（PNG 照片可能越压越大）→ 保留原图，不劣化。
		return data, mime, false
	}
	return out.Bytes(), outMime, true
}

// decodeAny 解码任意支持的图片格式（gif/jpeg/png/webp/bmp），返回图像与
// 实际解码格式名。webp/bmp 需 x/image（纯 Go，无 cgo）。
func decodeAny(data []byte) (image.Image, string, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err == nil {
		return img, format, nil
	}
	// 标准库失败后尝试 x/image 扩展格式（webp/bmp）。
	if img, err := webp.Decode(bytes.NewReader(data)); err == nil {
		return img, "webp", nil
	}
	if img, err := bmp.Decode(bytes.NewReader(data)); err == nil {
		return img, "bmp", nil
	}
	return nil, "", fmt.Errorf("不支持的图片格式: %w", err)
}

// scaleImage 把图片等比缩放到 (nw, nh)，使用高质量双线性插值。
func scaleImage(src image.Image, nw, nh int) image.Image {
	if nw <= 0 || nh <= 0 {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// 简单双线性插值（纯 Go，无 cgo 依赖，跨平台一致）。
	sb := src.Bounds()
	sw := sb.Dx()
	sh := sb.Dy()
	if sw == 0 || sh == 0 {
		return src
	}
	for y := 0; y < nh; y++ {
		sy := float64(y) * float64(sh) / float64(nh)
		y0 := int(sy)
		if y0 >= sh {
			y0 = sh - 1
		}
		y1 := y0 + 1
		if y1 >= sh {
			y1 = y0
		}
		dy := sy - float64(y0)
		for x := 0; x < nw; x++ {
			sx := float64(x) * float64(sw) / float64(nw)
			x0 := int(sx)
			if x0 >= sw {
				x0 = sw - 1
			}
			x1 := x0 + 1
			if x1 >= sw {
				x1 = x0
			}
			dx := sx - float64(x0)
			c00 := src.At(sb.Min.X+x0, sb.Min.Y+y0)
			c10 := src.At(sb.Min.X+x1, sb.Min.Y+y0)
			c01 := src.At(sb.Min.X+x0, sb.Min.Y+y1)
			c11 := src.At(sb.Min.X+x1, sb.Min.Y+y1)
			dst.Set(x, y, bilinear(c00, c10, c01, c11, dx, dy))
		}
	}
	return dst
}

// bilinear 四邻域双线性插值（RGBA 分量级）。
func bilinear(c00, c10, c01, c11 color.Color, dx, dy float64) color.Color {
	r00, g00, b00, a00 := c00.RGBA()
	r10, g10, b10, a10 := c10.RGBA()
	r01, g01, b01, a01 := c01.RGBA()
	r11, g11, b11, a11 := c11.RGBA()
	lerp := func(a, b, c, d uint32) uint32 {
		// 先水平插值再垂直插值。
		top := float64(a)*(1-dx) + float64(b)*dx
		bot := float64(c)*(1-dx) + float64(d)*dx
		return uint32(top*(1-dy) + bot*dy)
	}
	return color.RGBA{
		R: uint8(lerp(r00, r10, r01, r11) >> 8),
		G: uint8(lerp(g00, g10, g01, g11) >> 8),
		B: uint8(lerp(b00, b10, b01, b11) >> 8),
		A: uint8(lerp(a00, a10, a01, a11) >> 8),
	}
}
