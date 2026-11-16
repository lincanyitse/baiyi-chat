package tool

import (
	"fmt"
	"sort"
	"sync"

	"github.com/lincanyitse/baiyi-chat/internal/llm"
)

// Registry 持有全部可用工具，并负责向模型暴露工具清单。
//
// 未启用的工具根本不出现在返回结果里——既省 token，也降低误用。
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	enabled map[string]bool
	order   []string // 注册顺序，保证下发给模型的 schema 顺序稳定
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{
		tools:   make(map[string]Tool),
		enabled: make(map[string]bool),
	}
}

// Register 注册工具。同名工具会覆盖并保留原顺序。
func (r *Registry) Register(t Tool) error {
	spec := t.Spec()
	if err := ValidateSpec(spec); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[spec.Name]; !exists {
		r.order = append(r.order, spec.Name)
	}
	r.tools[spec.Name] = t
	if _, ok := r.enabled[spec.Name]; !ok {
		r.enabled[spec.Name] = true // 默认启用
	}
	return nil
}

// Unregister 移除工具。
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.enabled, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// SetEnabled 启用或禁用工具。
func (r *Registry) SetEnabled(name string, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return fmt.Errorf("工具 %q 未注册", name)
	}
	r.enabled[name] = on
	return nil
}

// Get 按名称取已启用的工具。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.enabled[name] {
		return nil, false
	}
	t, ok := r.tools[name]
	return t, ok
}

// Names 返回已启用工具名，顺序与注册顺序一致。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.order))
	for _, n := range r.order {
		if r.enabled[n] {
			out = append(out, n)
		}
	}
	return out
}

// Specs 返回已启用工具的声明，供 llm.Request.Tools 使用。
func (r *Registry) Specs() []llm.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]llm.ToolSpec, 0, len(r.order))
	for _, n := range r.order {
		if !r.enabled[n] {
			continue
		}
		s := r.tools[n].Spec()
		out = append(out, llm.ToolSpec{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}
	return out
}

// All 返回全部已注册工具（含禁用的），用于管理界面。
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n])
	}
	return out
}

// Enabled 返回已启用的工具，供 agent 装配本轮工具集。
//
// 不能直接用 All：它含被禁用的工具，而 agent 会把传入的每个工具
// 都下发给模型——禁用就形同虚设。
func (r *Registry) Enabled() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, n := range r.order {
		if r.enabled[n] {
			out = append(out, r.tools[n])
		}
	}
	return out
}

// SortedNames 返回按字母排序的已启用工具名，用于日志与展示。
func (r *Registry) SortedNames() []string {
	names := r.Names()
	sort.Strings(names)
	return names
}
