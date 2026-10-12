// Package vision 提供本地图片识别的输入检测与消息构造能力。
//
// 背景：ContextLoom 通过 OpenAI 兼容的 chat/completions API 调用大模型（DeepSeek
// 官方 API 即采用 OpenAI 兼容格式）。当用户输入文本携带本地图片文件路径时，
// 本包负责按路径与扩展名判断是否需要识图，并把图片读入编码为 data URI
// （base64），组装成 OpenAI 兼容的多模态 content 片段列表：
//
//	content: [
//	  {"type": "text", "text": "..."},
//	  {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
//	]
//
// 职责边界（不依赖网络与配置，纯函数库，便于单元测试与复用）：
//
//  1. HasImagePath：仅凭文本判断是否可能出现图片路径，不读文件；
//  2. DetectImages：从文本提取真实存在的本地图片，读取并编码为 data URI；
//  3. BuildUserMessage / BuildImageParts：组装多模态消息 / 图片片段；
//  4. EventText / MessageFromEventText：会话事件日志（JSONL）持久化与恢复——
//     事件日志只存纯文本（图片以 "[图片: <绝对路径>]" 标记），恢复时按路径
//     重新读取文件，避免把 MB 级 base64 写进日志。
package vision

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ImageMIMETypes 图片扩展名 → MIME 类型映射（键统一小写）。
// 扩展名即"是否识图"的判定依据（用户需求：不传文件，按路径与后缀判断）。
var ImageMIMETypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

// MaxImageBytes 单张图片读取上限（默认 32MiB，与 DeepSeek 官方单图大小限制
// 对齐——官方文档：base64/外部 URL 单图最大 32 MiB、Files API 64 MiB）。
// 旧值 10MB 会让 10~32MB 的合法图片在历史恢复（MessageFromEventText）时
// 被判定"图片已失效"——文件明明还在，却因本地限制读不进来，多轮追问丢图。
// 用 var 而非 const，便于测试中临时调小验证超限逻辑。
var MaxImageBytes int64 = 32 << 20

// imageMarkerPrefix / imageMarkerSuffix 是事件日志中图片标记的固定格式：
//
//	[图片: /绝对/路径.png]
//
// 恢复（MessageFromEventText）按行解析该标记；格式必须保持稳定，
// 否则旧会话事件日志无法重建图片消息。
const (
	imageMarkerPrefix = "[图片: "
	imageMarkerSuffix = "]"
	missingMarker     = "[图片已失效: "
)

// Image 表示一张被识别出的本地图片及其编码结果。
type Image struct {
	Path    string // 解析后的文件路径（filepath.Clean 之后）
	MIME    string // MIME 类型，如 "image/png"
	DataURI string // data URI，形如 "data:image/png;base64,iVBOR..."
	Size    int64  // 文件字节数
}

// HasImagePath 轻量判断文本中是否出现图片路径候选（不读文件、不校验存在性）。
// 匹配规则：把文本切分为路径候选 token（引号包裹或空白分隔），若任一 token 的
// 扩展名（忽略大小写）命中 ImageMIMETypes 则返回 true。
// 用途：在真正读文件之前做廉价短路，避免每条输入都触发文件 IO。
func HasImagePath(text string) bool {
	for _, tok := range tokenize(text) {
		if _, ok := MIMEForPath(tok); ok {
			return true
		}
	}
	return false
}

// MIMEForPath 按路径扩展名（忽略大小写）返回 MIME 类型与是否命中。
func MIMEForPath(path string) (string, bool) {
	lower := strings.ToLower(path)
	for ext, mime := range ImageMIMETypes {
		if strings.HasSuffix(lower, ext) {
			return mime, true
		}
	}
	return "", false
}

// DetectImages 从用户输入文本中提取图片路径并读取为 data URI。
//
// 提取策略（按出现顺序扫描）：
//   - a) 双引号/单引号/反引号包裹的 token（可含空格）；
//   - b) 空白分隔的 token（剥除尾部标点 ,。;；)）等）；
//
// 对每个 token 过滤：扩展名（忽略大小写）命中 ImageMIMETypes 才算图片候选。
//
// 路径解析：绝对路径直接使用；相对路径基于 baseDir 解析（baseDir 为空时使用
// 当前工作目录）；支持 ~ 展开为用户主目录；最终 filepath.Clean 归一化。
//
// 校验与读取：文件不存在 / 非普通文件 / 不可读 → 静默跳过（用户随手打的
// 类似路径不应报错中断）；文件大小超过 MaxImageBytes → 返回错误（含路径与
// 大小，提示用户换小图）；同一路径（Clean 后）去重，结果按文本出现顺序排列。
func DetectImages(text, baseDir string) ([]Image, error) {
	baseDir, err := resolveBaseDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("解析图片路径：获取当前工作目录失败: %w", err)
	}

	var images []Image
	seen := make(map[string]bool) // Clean 后的路径去重
	for _, tok := range tokenize(text) {
		mime, ok := MIMEForPath(tok)
		if !ok {
			continue
		}
		path, ok := resolvePath(tok, baseDir)
		if !ok {
			continue // ~ 无法展开等场景，静默跳过
		}
		key := filepath.Clean(path)
		if seen[key] {
			continue
		}
		img, err := loadImageFile(key, mime)
		if err == errOversize {
			return nil, fmt.Errorf("图片 %s 大小 %d 字节超过上限 %d 字节", key, img.Size, MaxImageBytes)
		}
		if err != nil {
			continue // 不存在 / 非普通文件 / 不可读：静默跳过
		}
		seen[key] = true
		images = append(images, img)
	}
	return images, nil
}

// BuildImageParts 仅构造图片片段列表（不含文本），供任务消息组装时使用。
func BuildImageParts(images []Image) []types.ContentPart {
	parts := make([]types.ContentPart, 0, len(images))
	for _, img := range images {
		parts = append(parts, imagePart(img))
	}
	return parts
}

// BuildUserMessage 构造一条 user 角色消息：Content = 文本 + 图片片段列表；
// 无图片时 Content 退化为纯文本字符串（OpenAI 兼容的字符串形式，多模态时
// 必须整体是数组，字符串与数组不能混用）。
func BuildUserMessage(text string, images []Image) types.Message {
	if len(images) == 0 {
		return types.Message{Role: "user", Content: text}
	}
	parts := []types.ContentPart{{Type: "text", Text: text}}
	return types.Message{Role: "user", Content: append(parts, BuildImageParts(images)...)}
}

// EventText 生成会话事件日志（JSONL）持久化用的纯文本内容：
// 原文 + 每张图片一行 "[图片: <绝对路径>]" 标记。无图时返回原文。
// 设计取舍：事件日志不存 base64（图片可达 MB 级，会把日志撑爆），
// 只存路径标记，恢复时由 MessageFromEventText 按路径重新读取。
func EventText(text string, images []Image) string {
	if len(images) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	for _, img := range images {
		b.WriteString("\n")
		b.WriteString(imageMarkerPrefix)
		b.WriteString(img.Path)
		b.WriteString(imageMarkerSuffix)
	}
	return b.String()
}

// MessageFromEventText 从事件日志纯文本恢复 user 消息：
//   - 无图片标记 → 纯文本消息；
//   - 有标记 → 逐行解析，扩展名命中图片类型的标记行按路径重新读取文件
//     重建 image_url 片段（文件仍存在且未超限）；文件已丢失/不可读 →
//     保留 "[图片已失效: 路径]" 文本提示，让模型与用户知道原图无法恢复；
//   - 形似标记但扩展名非图片（如用户原文恰好含 "[图片: foo.txt]"）→
//     按普通文本行原样保留，不误解析。
func MessageFromEventText(text string) types.Message {
	if !strings.Contains(text, imageMarkerPrefix) {
		return types.Message{Role: "user", Content: text}
	}
	var textLines, missing []string
	var images []Image
	for _, line := range strings.Split(text, "\n") {
		if path, ok := parseImageMarker(line); ok {
			mime, isImage := MIMEForPath(path)
			if isImage {
				img, err := loadImageFile(path, mime)
				if err == nil {
					images = append(images, img)
					continue
				}
				missing = append(missing, path)
				continue
			}
			// 标记形似 "[图片: ...]" 但扩展名不是图片（如用户原文恰好
			// 含 "[图片: foo.txt]"）→ 视为普通文本行保留，不误解析。
		}
		textLines = append(textLines, line)
	}
	body := strings.Join(textLines, "\n")
	if len(missing) > 0 {
		for _, p := range missing {
			body += "\n" + missingMarker + p + imageMarkerSuffix
		}
	}
	return BuildUserMessage(body, images)
}

// ── 内部辅助 ──

// errOversize 文件超过 MaxImageBytes（DetectImages 需要报错，
// 其余读取失败静默跳过——语义不同，用哨兵错误区分）。
var errOversize = fmt.Errorf("图片超过大小上限")

// loadImageFile 读取单个图片文件并编码为 data URI。
// path 需已 Clean；mime 由调用方按扩展名判定（空表示非图片）。
// 返回 (img, err)：errOversize 表示超限；其余错误表示不可用。
//
// 大图（≥200KB）会在编码前经 CompressImage 按 DeepSeek 官方缩放标准
// （~800×800 总像素）本地压缩：请求体从 MB 级降为 KB 级，token 成本
// 与上游带宽同降；webp/bmp 转 JPEG，MIME 随之更新。小图/动图/不可解码
// 原样直传（不劣化、不误伤）。
func loadImageFile(path, mime string) (Image, error) {
	if mime == "" {
		return Image{}, fmt.Errorf("非图片扩展名: %s", path)
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return Image{}, fmt.Errorf("文件不可用: %s", path)
	}
	if st.Size() > MaxImageBytes {
		return Image{Path: path, MIME: mime, Size: st.Size()}, errOversize
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Image{}, err
	}
	compressed, outMIME, _ := CompressImage(data, mime)
	return Image{
		Path:    path,
		MIME:    outMIME,
		DataURI: "data:" + outMIME + ";base64," + base64.StdEncoding.EncodeToString(compressed),
		Size:    st.Size(),
	}, nil
}

func imagePart(img Image) types.ContentPart {
	return types.ContentPart{
		Type:     "image_url",
		ImageURL: &types.ImageURLPart{URL: img.DataURI},
	}
}

// resolveBaseDir 归一化基准目录：空时回退当前工作目录（绝对路径）。
func resolveBaseDir(baseDir string) (string, error) {
	if baseDir != "" {
		return baseDir, nil
	}
	return os.Getwd()
}

// resolvePath 解析图片候选路径：
//   - ~ 或 ~/xxx（含 Windows 反斜杠）展开为用户主目录（展开失败视为不可解析）；
//   - 相对路径基于 baseDir 拼接；
//   - 最终 filepath.Clean 归一化。
func resolvePath(raw, baseDir string) (string, bool) {
	p := raw
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	return filepath.Clean(p), true
}

// parseImageMarker 解析图片标记行 "[图片: <path>]" → 返回路径与是否命中。
func parseImageMarker(line string) (string, bool) {
	if !strings.HasPrefix(line, imageMarkerPrefix) || !strings.HasSuffix(line, imageMarkerSuffix) {
		return "", false
	}
	p := strings.TrimSuffix(strings.TrimPrefix(line, imageMarkerPrefix), imageMarkerSuffix)
	if p == "" {
		return "", false
	}
	return p, true
}

// punctTrimSuffix 是 token 尾部需要剥除的标点集合（含全角与引号）。
const punctTrimSuffix = ",，.。;；:：!！?？)）]】}》>\"'`”’"

// leadingTrim 是 token 前导需要剥除的字符（ASCII 引号由 tokenizer 作为分隔符
// 处理不会出现在 token 开头，但括号/全角引号会——如 (./a.png)、“a.png”）。
const leadingTrim = "(（[【“‘"

// tokenize 把文本切分为路径候选 token：
//   - 双引号/单引号/反引号包裹的内容整体作为一个 token（可含空格），
//     内容再剥除两端标点（如 "a.png," 中的尾部逗号）；
//   - 其余按空白分隔，剥除尾部标点（含全角）与前导全角引号；
//   - 剥空或包裹为空的内容丢弃。
func tokenize(text string) []string {
	var tokens []string
	runes := []rune(text)
	for i := 0; i < len(runes); {
		r := runes[i]
		if isSpace(r) {
			i++
			continue
		}
		if r == '"' || r == '\'' || r == '`' {
			// 引号包裹 token：读到配对闭合或文本末尾。
			j := i + 1
			for j < len(runes) && runes[j] != r {
				j++
			}
			if tok := trimPunct(string(runes[i+1 : j])); tok != "" {
				tokens = append(tokens, tok)
			}
			i = j + 1
			continue
		}
		// 空白分隔 token：扫到下一个空白为止。
		j := i
		for j < len(runes) && !isSpace(runes[j]) {
			j++
		}
		if tok := trimPunct(string(runes[i:j])); tok != "" {
			tokens = append(tokens, tok)
		}
		i = j
	}
	return tokens
}

// isSpace 判断 rune 是否为空白字符（空格、制表、换行、回车）。
func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// trimPunct 剥除 token 尾部标点（含全角）与前导括号/全角引号。
func trimPunct(tok string) string {
	tok = strings.TrimRightFunc(tok, func(r rune) bool {
		return strings.ContainsRune(punctTrimSuffix, r)
	})
	return strings.TrimLeftFunc(tok, func(r rune) bool {
		return strings.ContainsRune(leadingTrim, r)
	})
}
