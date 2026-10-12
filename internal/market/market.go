// Package market 提供五品类市场（技能/工具链/员工/团队模板/场景）：
// 内置 catalog + 远程 URL catalog + 搜索与安装，
// 对标业界同类 Agent 的插件/技能生态，零依赖轻量落地。
//
// 设计原则：
//   - 内置 catalog 开箱即用（8 个示例技能），远程 URL catalog 可扩展社区分发；
//   - 安装即按品类写入入口文件 <dir>/<name>/<FileName>（与各品类既有包布局一致）：
//     skill→SKILL.md、toolchain→toolchain.yaml、employee→employee.yaml、
//     team→team.yaml、scene→scene.yaml；
//   - Kind 为空按 skill 处理（向后兼容旧数据）；
//   - 全部纯 Go 标准库，可交叉编译。
package market

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 市场五品类（§4.4.3 市场五品类）。
const (
	KindSkill     = "skill"     // 技能 → SKILL.md
	KindToolchain = "toolchain" // 工具链 → toolchain.yaml
	KindEmployee  = "employee"  // 员工 → employee.yaml
	KindTeam      = "team"      // 团队模板 → team.yaml
	KindScene     = "scene"     // 场景 → scene.yaml
)

// Item 市场条目（内置或远程 catalog 均此格式）。
type Item struct {
	Name        string   `json:"name"`              // 条目名（目录名，小写连字符）
	Kind        string   `json:"kind,omitempty"`    // 品类：skill/toolchain/employee/team/scene（空=skill，向后兼容）
	Description string   `json:"description"`       // 一句话描述
	Version     string   `json:"version,omitempty"` // 版本（如 1.0.0）
	Source      string   `json:"source,omitempty"`  // 来源标记：builtin / 远程 URL
	Author      string   `json:"author,omitempty"`  // 作者
	Content     string   `json:"content"`           // 入口文件正文（SKILL.md 含 frontmatter；yaml 品类为 yaml 正文）
	Tags        []string `json:"tags,omitempty"`    // 标签（搜索辅助）
}

// FileName 返回品类对应的入口文件名（未知品类返回空串，由 Install 校验报错）。
func (it Item) FileName() string {
	switch it.Kind {
	case "", KindSkill:
		return "SKILL.md"
	case KindToolchain:
		return "toolchain.yaml"
	case KindEmployee:
		return "employee.yaml"
	case KindTeam:
		return "team.yaml"
	case KindScene:
		return "scene.yaml"
	default:
		return ""
	}
}

// IsSkill 判断条目是否为技能品类（Kind 为空或 skill，向后兼容旧数据）。
func (it Item) IsSkill() bool {
	return it.Kind == "" || it.Kind == KindSkill
}

// validKind 校验品类是否合法（空=skill）。
func (it Item) validKind() bool {
	switch it.Kind {
	case "", KindSkill, KindToolchain, KindEmployee, KindTeam, KindScene:
		return true
	}
	return false
}

// Catalog 市场目录。
type Catalog struct {
	Name  string `json:"name"`
	Items []Item `json:"items"`
}

// Market 技能市场。
type Market struct {
	builtin Catalog
	remote  []Catalog
	client  *http.Client
}

// New 创建市场（内置 catalog 就绪）。
func New() *Market {
	m := &Market{
		builtin: builtinCatalog(),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
	return m
}

// AddRemote 添加远程 catalog URL（重复 URL 忽略）。
func (m *Market) AddRemote(url string) error {
	if url == "" {
		return fmt.Errorf("远程 catalog URL 为空")
	}
	for _, c := range m.remote {
		if c.Name == url {
			return nil // 已存在
		}
	}
	cat, err := m.fetchCatalog(url)
	if err != nil {
		return err
	}
	m.remote = append(m.remote, cat)
	return nil
}

// fetchCatalog 从 URL 拉取并解析 catalog。
func (m *Market) fetchCatalog(url string) (Catalog, error) {
	resp, err := m.client.Get(url)
	if err != nil {
		return Catalog{}, fmt.Errorf("拉取 catalog 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Catalog{}, fmt.Errorf("catalog HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB 上限
	if err != nil {
		return Catalog{}, err
	}
	var cat Catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return Catalog{}, fmt.Errorf("catalog 解析失败: %w", err)
	}
	if cat.Name == "" {
		cat.Name = url
	}
	for i := range cat.Items {
		it := &cat.Items[i]
		it.Source = url
		if it.Version == "" {
			it.Version = "0.0.0"
		}
	}
	return cat, nil
}

// Search 按名称/描述/标签模糊匹配（子串，不区分大小写）。
// 返回去重后的条目列表（内置 + 远程合并）。
func (m *Market) Search(query string) []Item {
	q := strings.ToLower(strings.TrimSpace(query))
	seen := make(map[string]bool)
	var out []Item
	add := func(c Catalog) {
		for _, it := range c.Items {
			if seen[it.Name] {
				continue
			}
			if q == "" || matchItem(it, q) {
				seen[it.Name] = true
				out = append(out, it)
			}
		}
	}
	add(m.builtin)
	for _, c := range m.remote {
		add(c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func matchItem(it Item, q string) bool {
	if strings.Contains(strings.ToLower(it.Name), q) {
		return true
	}
	if strings.Contains(strings.ToLower(it.Description), q) {
		return true
	}
	if strings.Contains(strings.ToLower(it.Kind), q) {
		return true
	}
	for _, t := range it.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

// Get 按名称精确查找条目（内置优先，其次远程）。
func (m *Market) Get(name string) (Item, bool) {
	for _, it := range m.builtin.Items {
		if it.Name == name {
			return it, true
		}
	}
	for _, c := range m.remote {
		for _, it := range c.Items {
			if it.Name == name {
				return it, true
			}
		}
	}
	return Item{}, false
}

// Install 安装条目到目标目录：按品类写入入口文件 <dir>/<name>/<FileName>。
// Kind 为空按 skill 处理（向后兼容旧数据）；未知 Kind 返回错误。
// 已存在同名入口文件时返回错误（不覆盖，防误伤）。
func (m *Market) Install(name, dir string) error {
	it, ok := m.Get(name)
	if !ok {
		return fmt.Errorf("市场不存在条目 %q（可用 Search 查看）", name)
	}
	if !it.validKind() {
		return fmt.Errorf("未知品类 %q（支持：%s/%s/%s/%s/%s），无法安装 %q", it.Kind,
			KindSkill, KindToolchain, KindEmployee, KindTeam, KindScene, it.Name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建安装目录失败: %w", err)
	}
	itemDir := filepath.Join(dir, it.Name)
	target := filepath.Join(itemDir, it.FileName())
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("条目 %q 已存在（%s），如需覆盖请先手动删除", it.Name, target)
	}
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, []byte(it.Content), 0o644); err != nil {
		return fmt.Errorf("写入条目失败: %w", err)
	}
	return nil
}

// ListInstalled 列出目标目录下已安装的条目名（识别五品类入口文件）。
func ListInstalled(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	entryFiles := []string{"SKILL.md", "toolchain.yaml", "employee.yaml", "team.yaml", "scene.yaml"}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, fn := range entryFiles {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), fn)); err == nil {
				out = append(out, e.Name())
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
