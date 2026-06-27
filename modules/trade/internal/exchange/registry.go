package exchange

import (
	"fmt"
	"sort"
	"sync"
)

// Factory 创建某交易所 ExchangeAdapter 的工厂函数。
type Factory func() ExchangeAdapter

var (
	mu       sync.RWMutex
	registry = make(map[string]Factory)
)

// Register 注册交易所适配器工厂。通常在各交易所包 init() 中调用。
// 重复注册同名交易所会 panic，以便在启动期暴露冲突。
func Register(name string, f Factory) {
	if name == "" || f == nil {
		panic("exchange: Register requires non-empty name and factory")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("exchange: duplicate registration for %q", name))
	}
	registry[name] = f
}

// New 按交易所名创建一个新的适配器实例。
func New(name string) (ExchangeAdapter, error) {
	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("exchange: unknown exchange %q", name)
	}
	return f(), nil
}

// Names 返回已注册的交易所名（按字典序）。
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
