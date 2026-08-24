package tools

import (
	"errors"
	"strings"

	toolv1 "yufeng/proto/gen/toolv1"
)

// Implementation 声明一个已编进服务端的工具原语。
type Implementation struct {
	Name   string
	Effect toolv1.ToolEffect
	Replay toolv1.ToolReplay
}

// Registry 保存服务端已注册原语；它不承担工具目录发布。
type Registry struct {
	items map[string]Implementation
}

// NewRegistry 从显式实现集合构造注册表，拒绝空名、重复名和未声明执行语义。
func NewRegistry(items []Implementation) (*Registry, error) {
	registry := &Registry{items: make(map[string]Implementation, len(items))}
	for _, item := range items {
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			return nil, errors.New("tool implementation name is empty")
		}
		if item.Effect == toolv1.ToolEffect_TOOL_EFFECT_UNSPECIFIED || item.Replay == toolv1.ToolReplay_TOOL_REPLAY_UNSPECIFIED {
			return nil, errors.New("tool implementation semantics are unspecified")
		}
		if _, exists := registry.items[item.Name]; exists {
			return nil, errors.New("tool implementation is registered twice")
		}
		registry.items[item.Name] = item
	}
	return registry, nil
}

// Lookup 返回名字对应的已编译实现。
func (r *Registry) Lookup(name string) (Implementation, bool) {
	if r == nil {
		return Implementation{}, false
	}
	item, ok := r.items[name]
	return item, ok
}
