package types

import (
	"fmt"
	"sync"
)

// HookChain 钩子函数链
type HookChain struct {
	mu    sync.RWMutex
	hooks []HookFunc
}

// NewHookChain 创建钩子链
func NewHookChain() *HookChain {
	return &HookChain{
		hooks: make([]HookFunc, 0),
	}
}

// Add 添加钩子函数
func (hc *HookChain) Add(hook HookFunc) {
	if hook == nil {
		return
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.hooks = append(hc.hooks, hook)
}

// Execute 执行钩子链
func (hc *HookChain) Execute(msg *Message) error {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	for i, hook := range hc.hooks {
		if err := hook(msg); err != nil {
			return fmt.Errorf("hook %d failed: %w", i, err)
		}
	}
	return nil
}

// Len 返回钩子数量
func (hc *HookChain) Len() int {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return len(hc.hooks)
}

// Clear 清空钩子链
func (hc *HookChain) Clear() {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.hooks = make([]HookFunc, 0)
}
