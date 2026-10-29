package factorudf

import "time"

// Limits 是模块检查与执行时的限制。零值字段取默认值（见 WithDefaults）。
type Limits struct {
	// MaxModuleBytes 是 wasm 模块大小上限。
	MaxModuleBytes int
	// MaxInitialPages 是声明的初始内存页数上限（每页 64 KiB）；MaxTableElems 是表的初始元素数上限。
	MaxInitialPages uint32
	MaxTableElems   uint32
	// MemoryLimitPages 是实例线性内存的页数上限（运行时强制，超过就让 memory.grow 失败）。
	MemoryLimitPages uint32
	// CallTimeout 是实例化与每次 guest 调用（包括 reserve、abi）的截止时间。
	CallTimeout time.Duration
	// BatchBytes 是单批输入输出的字节预算（决定每批序列数）。
	BatchBytes int
	// PoolSize 是每个模块实例池的上限；IdleTimeout 是实例空闲多久后关闭。
	PoolSize    int
	IdleTimeout time.Duration
}

// 默认值（设计 11.2；上线前用典型 UDF 实测后可调）。
const (
	DefaultMaxModuleBytes  = 1536 << 10
	DefaultMaxInitialPages = 256
	DefaultMaxTableElems   = 1024
	DefaultMemoryPages     = 512 << 4 // 512 MiB
	DefaultCallTimeout     = 10 * time.Second
	DefaultBatchBytes      = 64 << 20
	DefaultPoolSize        = 8
	DefaultIdleTimeout     = 5 * time.Minute
)

// WithDefaults 返回补全默认值后的限制。
func (l Limits) WithDefaults() Limits {
	if l.MaxModuleBytes <= 0 {
		l.MaxModuleBytes = DefaultMaxModuleBytes
	}
	if l.MaxInitialPages == 0 {
		l.MaxInitialPages = DefaultMaxInitialPages
	}
	if l.MaxTableElems == 0 {
		l.MaxTableElems = DefaultMaxTableElems
	}
	if l.MemoryLimitPages == 0 {
		l.MemoryLimitPages = DefaultMemoryPages
	}
	if l.CallTimeout <= 0 {
		l.CallTimeout = DefaultCallTimeout
	}
	if l.BatchBytes <= 0 {
		l.BatchBytes = DefaultBatchBytes
	}
	if l.PoolSize <= 0 {
		l.PoolSize = DefaultPoolSize
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = DefaultIdleTimeout
	}
	return l
}
