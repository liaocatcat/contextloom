package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/contextloom/contextloom-agent/pkg/types"
	"gopkg.in/yaml.v3"
)

type Manager struct {
	mu      sync.RWMutex
	dir     string
	plugins map[string]*Plugin
}

func New(dir string) *Manager {
	return &Manager{
		dir:     dir,
		plugins: make(map[string]*Plugin),
	}
}

func (m *Manager) Load(name string, manifest *Manifest) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p := &Plugin{
		Name:     name,
		Manifest: manifest,
		Status:   "loaded",
	}
	m.plugins[name] = p
	return nil
}

func (m *Manager) List() []*Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		result = append(result, p)
	}
	return result
}

func (m *Manager) Get(name string) (*Plugin, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.plugins[name]
	return p, ok
}

func (m *Manager) RunPreHooks() []types.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var snippets []types.Message
	for _, p := range m.plugins {
		if !p.Manifest.HasStage(string(types.StagePre)) {
			continue
		}
		if inject := p.Manifest.GetPreInject(); inject != "" {
			snippets = append(snippets, types.Message{
				Role:    "system", // 用 system role（OpenAI API 合法 role）
				Content: inject,
			})
		}
	}
	return snippets
}

func (m *Manager) RunDuringHooks() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var hooks []string
	for _, p := range m.plugins {
		if !p.Manifest.HasStage(string(types.StageDuring)) {
			continue
		}
		hooks = append(hooks, p.Manifest.Hooks.During.PreTool)
		hooks = append(hooks, p.Manifest.Hooks.During.PostTool)
	}
	return hooks
}

func (m *Manager) RunPostHooks() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var hooks []string
	for _, p := range m.plugins {
		if !p.Manifest.HasStage(string(types.StagePost)) {
			continue
		}
		hooks = append(hooks, p.Manifest.Hooks.Post.Finalize)
	}
	return hooks
}

// ScriptRef 脚本引用（插件名 + 完整脚本路径）
type ScriptRef struct {
	PluginName string
	ScriptPath string
}

// RunDuringScripts 返回 during 阶段所有脚本的完整路径（pre_tool + post_tool）
func (m *Manager) RunDuringScripts() []ScriptRef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var refs []ScriptRef
	for _, p := range m.plugins {
		if !p.Manifest.HasStage(string(types.StageDuring)) {
			continue
		}
		if s := p.Manifest.Hooks.During.PreTool; s != "" {
			refs = append(refs, ScriptRef{PluginName: p.Name, ScriptPath: filepath.Join(p.Dir, s)})
		}
		if s := p.Manifest.Hooks.During.PostTool; s != "" {
			refs = append(refs, ScriptRef{PluginName: p.Name, ScriptPath: filepath.Join(p.Dir, s)})
		}
	}
	return refs
}

// RunPostScripts 返回 post 阶段 finalize 脚本的完整路径
func (m *Manager) RunPostScripts() []ScriptRef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var refs []ScriptRef
	for _, p := range m.plugins {
		if !p.Manifest.HasStage(string(types.StagePost)) {
			continue
		}
		if s := p.Manifest.Hooks.Post.Finalize; s != "" {
			refs = append(refs, ScriptRef{PluginName: p.Name, ScriptPath: filepath.Join(p.Dir, s)})
		}
	}
	return refs
}

// ── v2 扩展：每轮注入 / 路由决策 / 插件工具注册 ──

// MessageInjectRef 每轮消息后注入钩子引用（v2 hooks.message.inject）。
type MessageInjectRef struct {
	PluginName string
	ScriptPath string
}

// RouteRef 路由决策钩子引用（v2 hooks.route.script）。
type RouteRef struct {
	PluginName string
	ScriptPath string
}

// ToolRegistration 插件工具注册项（v2 tools[].script 真实注册）。
type ToolRegistration struct {
	PluginName  string
	Name        string
	Description string
	Schema      map[string]any
	ScriptPath  string
	TimeoutSec  int // 0 = 默认 30s
}

// MessageInjects 返回所有声明了 hooks.message.inject 的插件引用（加载顺序）。
func (m *Manager) MessageInjects() []MessageInjectRef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var refs []MessageInjectRef
	for _, p := range m.plugins {
		if p.Manifest.HasMessageHook() {
			refs = append(refs, MessageInjectRef{
				PluginName: p.Name,
				ScriptPath: filepath.Join(p.Dir, p.Manifest.Hooks.Message.Inject),
			})
		}
	}
	return refs
}

// RouteScripts 返回所有声明了 hooks.route.script 的插件引用（加载顺序）。
func (m *Manager) RouteScripts() []RouteRef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var refs []RouteRef
	for _, p := range m.plugins {
		if p.Manifest.HasRouteHook() {
			refs = append(refs, RouteRef{
				PluginName: p.Name,
				ScriptPath: filepath.Join(p.Dir, p.Manifest.Hooks.Route.Script),
			})
		}
	}
	return refs
}

// ToolRegistrations 返回所有声明了 tools[].script 的插件工具注册项。
// 仅返回带脚本实现的工具（纯声明无脚本的保持元数据语义，不注册）。
func (m *Manager) ToolRegistrations() []ToolRegistration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var regs []ToolRegistration
	for _, p := range m.plugins {
		for _, t := range p.Manifest.Tools {
			if t.Script == "" {
				continue // 无脚本实现：不注册为可调用工具
			}
			params := t.Parameters
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			regs = append(regs, ToolRegistration{
				PluginName:  p.Name,
				Name:        t.Name,
				Description: t.Description,
				Schema:      params,
				ScriptPath:  filepath.Join(p.Dir, t.Script),
				TimeoutSec:  t.ToolTimeout(),
			})
		}
	}
	return regs
}

func (m *Manager) Unload(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.plugins, name)
}

func (m *Manager) LoadFromDir(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // 目录不存在不算错误，静默跳过
		}
		return 0, fmt.Errorf("read plugin dir %s: %w", dir, err)
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		manifestPath := filepath.Join(dir, entry.Name(), "manifest.yaml")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		var manifest Manifest
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			continue
		}

		name := manifest.Name
		if name == "" {
			name = entry.Name()
		}

		// 直接创建 Plugin 并设置 Dir（记录插件目录供脚本执行拼接路径）
		m.mu.Lock()
		m.plugins[name] = &Plugin{
			Name:     name,
			Dir:      filepath.Join(dir, entry.Name()),
			Manifest: &manifest,
			Status:   "loaded",
		}
		m.mu.Unlock()
		count++
	}

	return count, nil
}

type Plugin struct {
	Name     string
	Dir      string // 插件目录绝对路径，供脚本执行拼接路径
	Manifest *Manifest
	Status   string
}

func (p *Plugin) String() string {
	return fmt.Sprintf("%s (%s) [%s]", p.Name, p.Manifest.Version, p.Status)
}
