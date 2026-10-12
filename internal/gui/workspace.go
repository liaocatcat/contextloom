// Package gui —— 工具链体系升级方案 §4.7「GUI 全配置化」的后端支撑：
// 员工 / 场景 / 工具链 / 门禁 四类对象的 GUI CRUD。
//
// 设计目标：前端全程鼠标完成配置，YAML 只是收据。
// 文件布局与 orchestrator/scenes/toolchain 包一致：
//
//   - 员工：  <employees_dir>/<name>/employee.yaml
//   - 场景：  <scenes_dir>/<name>/scene.yaml
//   - 工具链：<chains_dir>/<name>/toolchain.yaml
//   - 门禁：  <gates_dir>/gate.yaml（level + pass_score）、
//     <gates_dir>/quality_ledger.yaml（只读展示，不做 write CRUD）
//
// 目录来源：ConfigSnapshot 的四目录键（employees_dir/scenes_dir/chains_dir/
// gates_dir），缺失时回退 rt.ProjectRoot()/.contextloom/<品类>（兼容旧版 GUI
// 与测试桩）。所有文件路径经 safePath 相对四目录做越界校验；name 拒绝空串
// 与含 /、..、\ 的字符串；写文件 0644、目录 0755；绝不读 .contextloom 之外。
//
// 本文件只用标准库 + gopkg.in/yaml.v3，零新增依赖，跨平台（无 cgo）。
// 序列化用 map[string]any 直接读写，尽量保留用户 YAML 原文与注释。
package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// objectDirs 从 ConfigSnapshot 取四目录；缺键时回退
// s.rt.ProjectRoot()+"/.contextloom/"+<品类名>。
// keys: employees / scenes / chains / gates。
func (s *Server) objectDirs() map[string]string {
	dirs := map[string]string{
		"employees": "employees",
		"scenes":    "scenes",
		"chains":    "chains",
		"gates":     "gates",
	}
	snapKeys := map[string]string{
		"employees": "employees_dir",
		"scenes":    "scenes_dir",
		"chains":    "chains_dir",
		"gates":     "gates_dir",
	}
	snap := s.rt.ConfigSnapshot()
	root := s.rt.ProjectRoot()
	if root == "" {
		root = "."
	}
	for kind, key := range snapKeys {
		if v, ok := snap[key].(string); ok && strings.TrimSpace(v) != "" {
			dirs[kind] = v
			continue
		}
		dirs[kind] = filepath.Join(root, ".contextloom", dirs[kind])
	}
	return dirs
}

// workspaceAbsDir 把品类目录归一化为绝对路径（safePath 要求基座绝对路径）。
func workspaceAbsDir(dir string) (string, error) {
	base := strings.TrimSpace(dir)
	if base == "" {
		base = "."
	}
	return filepath.Abs(base)
}

// validateObjectName 校验对象名：非空、不含 / \ ..（防目录穿越）。
func validateObjectName(kind, name string) error {
	t := strings.TrimSpace(name)
	if t != name || t == "" {
		return fmt.Errorf("%s name 不能为空或含首尾空白", kind)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("%s name 非法（不能包含 /、\\、..）: %s", kind, name)
	}
	return nil
}

// objectFileValidate 校验 YAML 文本 + 传入 name 与 YAML 内 name 环一致。
// clobber 为 true 时表示允许用传入 name 回填缺失的名字（save-first 语义）。
// 返回 (字节, error)。原文首尾空白保留（get 必须与 save 输入一致）。
func objectFileValidate(kind, name, yamlText string, clobber bool) ([]byte, error) {
	if strings.TrimSpace(yamlText) == "" {
		return nil, fmt.Errorf("%s yaml 不能为空", kind)
	}
	doc, err := yamlNode(yamlText)
	if err != nil {
		return nil, err
	}
	if doc == nil || doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s yaml 必须是顶层映射且非空", kind)
	}
	var m map[string]any
	if err := doc.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s yaml 解析失败: %w", kind, err)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("%s yaml 为空映射", kind)
	}
	if got, ok := m["name"].(string); ok && strings.TrimSpace(got) != "" && strings.TrimSpace(got) != name {
		return nil, fmt.Errorf("%s 档案内 name %q 与传入 name %q 不一致", kind, got, name)
	}
	if clobber {
		if _, ok := m["name"]; !ok {
			m["name"] = name
			data, err := yaml.Marshal(m)
			if err != nil {
				return nil, fmt.Errorf("%s yaml 重新序列化失败: %w", kind, err)
			}
			return data, nil
		}
	}
	return []byte(yamlText), nil
}

// yamlNode 用 yaml.v3 解析 + 校验文本（顶层必须是 MappingNode 且含内容）。
func yamlNode(text string) (*yaml.Node, error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(text), &node); err != nil {
		return nil, fmt.Errorf("无效 YAML: %w", err)
	}
	if node.Kind == 0 || len(node.Content) == 0 {
		return nil, fmt.Errorf("无效 YAML：文档为空")
	}
	return node.Content[0], nil
}

// writeObjectFile 新建/覆盖对象文件（仅当 force 或目标不存在时）。
func (s *Server) writeObjectFile(p, text string, force bool) (string, error) {
	if !force {
		if _, err := os.Stat(p); err == nil {
			return "", fmt.Errorf("已存在，请用 GUI 编辑后保存或 force:true")
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("写入失败: %w", err)
	}
	return p, nil
}

// parseYAMLMap 解析 YAML 文本为 map（解析失败返回错误）。
func parseYAMLMap(kind, text string) (map[string]any, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(text), &m); err != nil {
		return nil, fmt.Errorf("%s YAML 解析失败: %w", kind, err)
	}
	if m == nil {
		return nil, fmt.Errorf("%s YAML 为空映射", kind)
	}
	return m, nil
}

// strField 取 map 中字符串字段（缺失/非字符串返回 ""）。
func strField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

// getYAML 按类别读取单个对象 YAML 原文。
func (s *Server) getYAML(kind, name string) (any, error) {
	if err := validateObjectName(kind, name); err != nil {
		return nil, err
	}
	dir := s.objectDirs()[kind]
	abs, err := workspaceAbsDir(dir)
	if err != nil {
		return nil, err
	}
	entry := map[string]string{
		"employees": "employee.yaml",
		"scenes":    "scene.yaml",
		"chains":    "toolchain.yaml",
	}[kind]
	p, err := s.safePath(name, abs)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(p, entry))
	if err != nil {
		return nil, fmt.Errorf("%s 不存在: %s", kind, name)
	}
	return map[string]any{"name": name, "yaml": string(data)}, nil
}

// saveYAML 校验 + 写入单个对象 YAML。
func (s *Server) saveYAML(kind, name, yamlText string, force bool) (any, error) {
	if err := validateObjectName(kind, name); err != nil {
		return nil, err
	}
	data, err := objectFileValidate(kind, name, yamlText, true)
	if err != nil {
		return nil, err
	}
	dir := s.objectDirs()[kind]
	abs, err := workspaceAbsDir(dir)
	if err != nil {
		return nil, err
	}
	entry := map[string]string{
		"employees": "employee.yaml",
		"scenes":    "scene.yaml",
		"chains":    "toolchain.yaml",
	}[kind]
	p, err := s.safePath(name, abs)
	if err != nil {
		return nil, err
	}
	final := filepath.Join(p, entry)
	written, err := s.writeObjectFile(final, string(data), force)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": written}, nil
}

// deleteObject 删除品类对象：仅删除目标入口 YAML 文件；
// 若该对象目录下无其他文件，则一并移除目录。
func (s *Server) deleteObject(kind, name string) (any, error) {
	if err := validateObjectName(kind, name); err != nil {
		return nil, err
	}
	dir := s.objectDirs()[kind]
	abs, err := workspaceAbsDir(dir)
	if err != nil {
		return nil, err
	}
	entry := map[string]string{
		"employees": "employee.yaml",
		"scenes":    "scene.yaml",
		"chains":    "toolchain.yaml",
	}[kind]
	p, err := s.safePath(name, abs)
	if err != nil {
		return nil, err
	}
	final := filepath.Join(p, entry)
	if _, err := os.Stat(final); err != nil {
		return nil, fmt.Errorf("%s 不存在: %s", kind, name)
	}
	if err := os.Remove(final); err != nil {
		return nil, fmt.Errorf("删除失败: %w", err)
	}
	// 目录内无剩余文件才移除目录（仅限当前对象目录，绝不递归越界）
	remaining, _ := os.ReadDir(p)
	if len(remaining) == 0 {
		_ = os.Remove(p)
	}
	return map[string]any{"deleted": true}, nil
}

// listYAML 扫描品类目录（每个子目录 = 一个对象）返回摘要列表与警告。
func (s *Server) listYAML(kind string) (any, error) {
	dir := s.objectDirs()[kind]
	abs, err := workspaceAbsDir(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"items": []map[string]any{}}, nil
		}
		return nil, fmt.Errorf("读取 %s 目录失败: %w", kind, err)
	}
	entry := map[string]string{
		"employees": "employee.yaml",
		"scenes":    "scene.yaml",
		"chains":    "toolchain.yaml",
	}[kind]
	items := make([]map[string]any, 0, len(entries))
	var warnings []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(abs, e.Name(), entry)
		data, err := os.ReadFile(sub)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		m, err := parseYAMLMap(kind, string(data))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: YAML 解析失败: %v", e.Name(), err))
			continue
		}
		item := map[string]any{
			"name": e.Name(),
			"path": sub,
		}
		switch kind {
		case "employees":
			item["display"] = strField(m, "display")
			item["role"] = strField(m, "role")
		case "scenes":
			item["display"] = strField(m, "display")
			item["keywords"] = m["keywords"]
		case "chains":
			item["version"] = strField(m, "version")
			if steps, ok := m["steps"].([]any); ok {
				item["steps"] = len(steps)
			} else {
				item["steps"] = 0
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		a, _ := items[i]["name"].(string)
		b, _ := items[j]["name"].(string)
		return a < b
	})
	out := map[string]any{"items": items}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	return out, nil
}

// ─── 员工 employee.* ───

func (s *Server) employeeList() (any, error) {
	out, err := s.listYAML("employees")
	ret, _ := out.(map[string]any)
	if ret == nil {
		ret = map[string]any{}
	}
	if err != nil {
		return ret, err
	}
	if ret["items"] == nil {
		ret["items"] = []map[string]any{}
	}
	return ret, nil
}

func (s *Server) employeeGet(name string) (any, error) { return s.getYAML("employees", name) }

func (s *Server) employeeSave(name, yamlText string, force bool) (any, error) {
	return s.saveYAML("employees", name, yamlText, force)
}

func (s *Server) employeeDelete(name string) (any, error) { return s.deleteObject("employees", name) }

// ─── 场景 scene.* ───

func (s *Server) sceneList() (any, error) {
	out, err := s.listYAML("scenes")
	ret, _ := out.(map[string]any)
	if ret == nil {
		ret = map[string]any{}
	}
	if err != nil {
		return ret, err
	}
	if ret["items"] == nil {
		ret["items"] = []map[string]any{}
	}
	return ret, nil
}

func (s *Server) sceneGet(name string) (any, error) { return s.getYAML("scenes", name) }

func (s *Server) sceneSave(name, yamlText string, force bool) (any, error) {
	return s.saveYAML("scenes", name, yamlText, force)
}

func (s *Server) sceneDelete(name string) (any, error) { return s.deleteObject("scenes", name) }

// ─── 工具链 toolchain.* ───

func (s *Server) toolchainList() (any, error) {
	out, err := s.listYAML("chains")
	ret, _ := out.(map[string]any)
	if ret == nil {
		ret = map[string]any{}
	}
	if err != nil {
		return ret, err
	}
	if ret["items"] == nil {
		ret["items"] = []map[string]any{}
	}
	return ret, nil
}

func (s *Server) toolchainGet(name string) (any, error) { return s.getYAML("chains", name) }

func (s *Server) toolchainSave(name, yamlText string, force bool) (any, error) {
	return s.saveYAML("chains", name, yamlText, force)
}

func (s *Server) toolchainDelete(name string) (any, error) { return s.deleteObject("chains", name) }

// ─── 门禁 gate.*（无 delete：门禁只能启用/停用，用 level none 表示停用）───

const (
	gateFileName    = "gate.yaml"
	ledgerFileName  = "quality_ledger.yaml"
	gateDefaultLevl = "standard"
	gateDefaultScor = 90
)

// gateList 门禁状态：目录不存在 → {enabled:false}；
// 目录存在 → {enabled:true, level, pass_score, ledger_yaml(可选)}。
func (s *Server) gateList() (any, error) {
	dir := s.objectDirs()["gates"]
	abs, err := workspaceAbsDir(dir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return map[string]any{"enabled": false}, nil
	}
	out := map[string]any{"enabled": true, "level": gateDefaultLevl, "pass_score": gateDefaultScor}
	if data, err := os.ReadFile(filepath.Join(abs, gateFileName)); err == nil {
		if m, err := parseYAMLMap("gate", string(data)); err == nil {
			if lv := strField(m, "level"); lv != "" {
				out["level"] = strings.ToLower(lv)
			}
			switch v := m["pass_score"].(type) {
			case int:
				out["pass_score"] = v
			case int64:
				out["pass_score"] = int(v)
			case string:
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					out["pass_score"] = n
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(abs, ledgerFileName)); err == nil {
		out["ledger_yaml"] = string(data)
	}
	return out, nil
}

// gateSave 写门禁配置 <gates_dir>/gate.yaml。
// gates 目录创建即激活（runtime qualityGate 的"目录存在即启用"语义）。
func (s *Server) gateSave(level string, passScore int) (any, error) {
	lv := strings.ToLower(strings.TrimSpace(level))
	if lv != "standard" && lv != "fast" && lv != "none" {
		return nil, fmt.Errorf("无效 level %q：仅支持 standard/fast/none", level)
	}
	if passScore < 0 || passScore > 100 {
		return nil, fmt.Errorf("无效 pass_score %d：必须在 0-100 之间", passScore)
	}
	dir := s.objectDirs()["gates"]
	abs, err := workspaceAbsDir(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("创建门禁目录失败: %w", err)
	}
	p := filepath.Join(abs, gateFileName)
	text := fmt.Sprintf("level: %s\npass_score: %d\n", lv, passScore)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		return nil, fmt.Errorf("写入失败: %w", err)
	}
	return map[string]any{"path": p}, nil
}
