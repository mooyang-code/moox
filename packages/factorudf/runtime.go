package factorudf

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Budget 是调用方提供的内存预算接口：实例创建与线性内存增长之前申请，超预算返回错误；实例关闭时释放。
// 引擎接 memadmit，管理端的试算与发布各自接自己的预算。
type Budget interface {
	Acquire(bytes int64) error
	Release(bytes int64)
}

// unlimitedBudget 不限制（只用于没有配置预算的场景，例如本地门禁）。
type unlimitedBudget struct{}

func (unlimitedBudget) Acquire(int64) error { return nil }
func (unlimitedBudget) Release(int64)       {}

// ErrBudget 表示超出调用方的内存预算。
var ErrBudget = errors.New("超出自定义算子的内存预算")

// Options 是运行时的参数。
type Options struct {
	Limits Limits
	Budget Budget
	// CacheDir 非空时用目录做编译缓存：每个版本在每台机器上只编译一次。
	CacheDir string
}

// Runtime 持有 wazero 运行时与已加载的模块。
type Runtime struct {
	rt     wazero.Runtime
	limits Limits
	budget Budget
	cache  wazero.CompilationCache

	mu      sync.Mutex
	modules map[string]*Module
	closed  bool
}

// NewRuntime 创建运行时（编译模式）。
func NewRuntime(ctx context.Context, opts Options) (*Runtime, error) {
	l := opts.Limits.WithDefaults()
	var cache wazero.CompilationCache
	if opts.CacheDir != "" {
		var err error
		if cache, err = wazero.NewCompilationCacheWithDir(opts.CacheDir); err != nil {
			return nil, fmt.Errorf("打开编译缓存目录：%w", err)
		}
	} else {
		cache = wazero.NewCompilationCache()
	}
	b := opts.Budget
	if b == nil {
		b = unlimitedBudget{}
	}
	return &Runtime{
		rt: wazero.NewRuntimeWithConfig(ctx, newRuntimeConfig(l, cache)), limits: l, budget: b, cache: cache,
		modules: map[string]*Module{},
	}, nil
}

// Limits 返回生效的限制。
func (r *Runtime) Limits() Limits { return r.limits }

// Close 关闭所有模块与运行时。
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	mods := r.modules
	r.modules = map[string]*Module{}
	r.mu.Unlock()
	for _, m := range mods {
		m.pool.close(ctx)
	}
	err := r.rt.Close(ctx)
	if cerr := r.cache.Close(ctx); err == nil {
		err = cerr
	}
	return err
}

// Module 是一个已通过检查、编译好的版本，带自己的实例池。
type Module struct {
	rt       *Runtime
	Info     ModuleInfo
	compiled wazero.CompiledModule
	pool     *pool
}

// Load 做完整的模块检查（静态检查、编译、导出签名、ABI 版本）并加载；同一哈希重复加载返回已有模块。
func (r *Runtime) Load(ctx context.Context, wasm []byte) (*Module, error) {
	hash := Hash(wasm)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errClosed
	}
	if m := r.modules[hash]; m != nil {
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()

	info, err := Inspect(wasm, r.limits)
	if err != nil {
		return nil, err
	}
	compiled, err := r.rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, checkFail("编译失败（可能用了不被允许的特性）：%v", err)
	}
	if err := checkExports(compiled); err != nil {
		_ = compiled.Close(ctx)
		return nil, err
	}
	if err := checkABI(ctx, r.rt, compiled, r.limits); err != nil {
		_ = compiled.Close(ctx)
		return nil, err
	}
	m := &Module{rt: r, Info: *info, compiled: compiled}
	m.pool = newPool(m)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		_ = compiled.Close(ctx)
		return nil, errClosed
	}
	if existing := r.modules[hash]; existing != nil {
		_ = compiled.Close(ctx)
		return existing, nil
	}
	r.modules[hash] = m
	return m, nil
}

// Unload 关闭并移除一个模块（它的实例池一并关闭）。
func (r *Runtime) Unload(ctx context.Context, hash string) {
	r.mu.Lock()
	m := r.modules[hash]
	delete(r.modules, hash)
	r.mu.Unlock()
	if m != nil {
		m.pool.close(ctx)
		_ = m.compiled.Close(ctx)
	}
}

// Instance 是一个 wasm 实例。同一时刻只能被一个调用方使用。
type Instance struct {
	m         *Module
	mod       api.Module
	mem       api.Memory
	reserve   api.Function
	eval      api.Function
	accounted int64 // 已向预算申请的字节数（线性内存当前大小）
	scratch   int64 // 已经成功 reserve 过的最大字节数
	lastUsed  time.Time
	closed    bool
}

// MemoryBytes 返回实例线性内存的当前大小。
func (in *Instance) MemoryBytes() int64 { return int64(in.mem.Size()) }

// instantiate 创建实例：先向预算申请初始内存，再实例化（带截止时间）。
func (m *Module) instantiate(ctx context.Context) (*Instance, error) {
	initial := int64(m.Info.InitialPage) << 16
	if err := m.rt.budget.Acquire(initial); err != nil {
		return nil, fmt.Errorf("%w：%v", ErrBudget, err)
	}
	cctx, cancel := context.WithTimeout(ctx, m.rt.limits.CallTimeout)
	defer cancel()
	mod, err := m.rt.rt.InstantiateModule(cctx, m.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		m.rt.budget.Release(initial)
		return nil, &CallError{Reason: ReasonInstantiate, Err: err}
	}
	in := &Instance{m: m, mod: mod, mem: mod.Memory(), reserve: mod.ExportedFunction(exportReserve), eval: mod.ExportedFunction(exportEval), lastUsed: time.Now()}
	// 实际初始大小可能与声明不同（例如 data 段把内存撑大）：以实际为准补申请。
	in.accounted = initial
	if actual := int64(in.mem.Size()); actual != initial {
		if actual > initial {
			if err := m.rt.budget.Acquire(actual - initial); err != nil {
				in.accounted = initial
				in.close(ctx)
				return nil, fmt.Errorf("%w：%v", ErrBudget, err)
			}
		} else {
			m.rt.budget.Release(initial - actual)
		}
		in.accounted = actual
	}
	return in, nil
}

func (in *Instance) close(ctx context.Context) {
	if in.closed {
		return
	}
	in.closed = true
	_ = in.mod.Close(ctx)
	in.m.rt.budget.Release(in.accounted)
	in.accounted = 0
}

// 失败原因（CallError.Reason）。
const (
	ReasonInstantiate = "instantiate"
	ReasonTimeout     = "timeout"
	ReasonTrap        = "trap"
	ReasonMemory      = "memory"
	ReasonCode        = "code"
	ReasonBudget      = "budget"
	ReasonInput       = "input"
)

// CallError 是 guest 调用失败：trap、超时、内存、返回码。出错的实例会被丢弃。
type CallError struct {
	Reason string
	Code   int32
	Err    error
}

func (e *CallError) Error() string {
	switch e.Reason {
	case ReasonCode:
		switch e.Code {
		case 1:
			return "自定义算子拒绝了参数（参数越界）"
		case 2:
			return "自定义算子报告窗口长度不一致"
		}
		return fmt.Sprintf("自定义算子返回错误码 %d", e.Code)
	case ReasonTimeout:
		return "自定义算子执行超时：" + e.Err.Error()
	case ReasonMemory:
		return "自定义算子申请暂存区失败（内存不足或长度溢出）"
	case ReasonBudget:
		return e.Err.Error()
	}
	if e.Err != nil {
		return "自定义算子执行失败（" + e.Reason + "）：" + e.Err.Error()
	}
	return "自定义算子执行失败（" + e.Reason + "）"
}

func (e *CallError) Unwrap() error { return e.Err }

// classify 把 wazero 返回的错误分成超时与 trap。
func classify(ctx context.Context, err error) *CallError {
	if ctx.Err() != nil {
		return &CallError{Reason: ReasonTimeout, Err: ctx.Err()}
	}
	return &CallError{Reason: ReasonTrap, Err: err}
}
