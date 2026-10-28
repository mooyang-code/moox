package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/udfhost"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// udfModules 管理引擎上的自定义算子模块（设计 11.1）：本地按内容寻址存储（<目录>/<哈希>.wasm）、按版本哈希向管理端拉取并校验
// sha256、用 wazero 编译（编译缓存在 <目录>/cache，每个版本在每台机器上只编译一次）、记录各版本的状态供心跳上报。
// 目录同步、启动恢复、补算领取三个入口都经过 Prepare：先把引用的模块备齐，再使用定义。
type udfModules struct {
	dir   string
	host  *udfhost.Host
	fetch func(ctx context.Context, hash string) ([]byte, error)

	mu     sync.Mutex
	states map[string]*moduleState
}

type moduleState struct {
	name  string
	state string
	err   string
}

// admitBudget 把自定义算子实例的常驻内存登记到进程内存预算（memadmit）：实例创建与线性内存增长之前申请，
// 超出（预算 − 其他常驻）就拒绝，不只是登记；实例关闭时释放。
type admitBudget struct {
	admit *memadmit.Admitter
	mu    sync.Mutex
	used  int64
}

const udfResidentName = "udf_instances"

func (b *admitBudget) Acquire(bytes int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.admit.CanResident(udfResidentName, b.used+bytes) {
		return fmt.Errorf("自定义算子实例需要 %d 字节常驻内存，超出进程内存预算（已用 %d）", bytes, b.used)
	}
	b.used += bytes
	b.admit.SetResident(udfResidentName, b.used)
	return nil
}

func (b *admitBudget) Release(bytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used = max(b.used-bytes, 0)
	b.admit.SetResident(udfResidentName, b.used)
}

// newUDFModules 创建模块存储与宿主。admit 可为 nil（不限制）。
func newUDFModules(ctx context.Context, cfg UDFConfig, workers int, admit *memadmit.Admitter, fetch func(context.Context, string) ([]byte, error)) (*udfModules, error) {
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "cache"), 0o750); err != nil {
		return nil, fmt.Errorf("create udf directory: %w", err)
	}
	var budget factorudf.Budget
	if admit != nil {
		budget = &admitBudget{admit: admit}
	}
	runtime, err := factorudf.NewRuntime(ctx, factorudf.Options{
		CacheDir: filepath.Join(cfg.Dir, "cache"), Budget: budget,
		Limits: factorudf.Limits{
			CallTimeout: cfg.CallTimeout, MemoryLimitPages: uint32(int64(cfg.InstanceMemory) >> 16), BatchBytes: int(cfg.BatchBytes),
			PoolSize: max(workers, 1), IdleTimeout: cfg.IdleTimeout,
		},
	})
	if err != nil {
		return nil, err
	}
	return &udfModules{dir: cfg.Dir, host: udfhost.New(runtime), fetch: fetch, states: map[string]*moduleState{}}, nil
}

// Host 返回执行宿主（注入 pipeline）。
func (m *udfModules) Host() *udfhost.Host { return m.host }

// Close 关闭运行时。
func (m *udfModules) Close(ctx context.Context) error { return m.host.Runtime().Close(ctx) }

func (m *udfModules) set(hash, name, state, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[hash] = &moduleState{name: name, state: state, err: errText}
}

// Ready 报告版本的模块是否已拉取并编译好。
func (m *udfModules) Ready(hash string) bool { return m.host.Has(hash) }

func (m *udfModules) path(hash string) string { return filepath.Join(m.dir, hash+".wasm") }

// Ensure 备齐一个版本：本地有就读本地（校验哈希），没有就向管理端拉取，写盘后加载并编译。失败时记录状态与原因。
func (m *udfModules) Ensure(ctx context.Context, hash, name string) error {
	if m.host.Has(hash) {
		return nil
	}
	m.set(hash, name, domain.UDFModuleFetching, "")
	var fetchFailed bool
	err := m.host.Ensure(ctx, hash, func(ctx context.Context, hash string) ([]byte, error) {
		if wasm, err := os.ReadFile(m.path(hash)); err == nil && factorudf.Hash(wasm) == hash {
			return wasm, nil
		}
		wasm, err := m.fetch(ctx, hash)
		if err != nil {
			fetchFailed = true
			return nil, err
		}
		if factorudf.Hash(wasm) != hash {
			fetchFailed = true
			return nil, fmt.Errorf("拉取到的模块哈希是 %s，不是要求的 %s", factorudf.Hash(wasm)[:12], hash[:12])
		}
		if err := writeFileAtomic(m.path(hash), wasm); err != nil {
			log.WarnContextf(ctx, "factor_udf_module_save_failed hash=%s error=%v", hash, err)
		}
		return wasm, nil
	})
	switch {
	case err == nil:
		m.set(hash, name, domain.UDFModuleReady, "")
		log.InfoContextf(ctx, "factor_udf_module_ready name=%s hash=%s", name, hash)
		return nil
	case fetchFailed || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		m.set(hash, name, domain.UDFModuleFetchFailed, err.Error())
	default:
		m.set(hash, name, domain.UDFModuleCompileFailed, err.Error())
	}
	return err
}

// EnsureAll 备齐一批版本（名称取自定义里引用的名称）；任何一个失败就返回错误，已经备齐的保留。
func (m *udfModules) EnsureAll(ctx context.Context, refs []udfRef) error {
	var errs []error
	for _, ref := range refs {
		if err := m.Ensure(ctx, ref.hash, ref.name); err != nil {
			errs = append(errs, fmt.Errorf("自定义算子 %s@%.12s：%w", ref.name, ref.hash, err))
		}
	}
	return errors.Join(errs...)
}

// Status 返回已知各版本的状态（按名称、哈希排序）。
func (m *udfModules) Status() []domain.UDFModuleStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.UDFModuleStatus, 0, len(m.states))
	for hash, s := range m.states {
		out = append(out, domain.UDFModuleStatus{Hash: hash, Name: s.name, State: s.state, Error: s.err})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Hash < out[j].Hash
	})
	return out
}

// Retain 只保留当前目录引用的版本的状态与已加载模块（卸载不再引用的）；hashes 是仍被引用的版本。
func (m *udfModules) Retain(ctx context.Context, hashes map[string]bool) {
	for _, hash := range m.host.Hashes() {
		if !hashes[hash] {
			m.host.Unload(ctx, hash)
		}
	}
	m.mu.Lock()
	for hash := range m.states {
		if !hashes[hash] {
			delete(m.states, hash)
		}
	}
	m.mu.Unlock()
}

// udfRef 是目录里引用的一个自定义算子版本。
type udfRef struct{ name, hash string }

// refsOfTasks 汇总任务成员定义引用的版本（去重，按名称、哈希排序）。
func refsOfTasks(tasks []domain.EngineTask) []udfRef {
	seen := map[udfRef]bool{}
	var refs []udfRef
	for _, task := range tasks {
		for _, member := range task.Members {
			for _, udf := range member.Factor.UDFs {
				ref := udfRef{name: udf.Name, hash: udf.Hash}
				if udf.Hash != "" && !seen[ref] {
					seen[ref] = true
					refs = append(refs, ref)
				}
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].name != refs[j].name {
			return refs[i].name < refs[j].name
		}
		return refs[i].hash < refs[j].hash
	})
	return refs
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wasm-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// 模块准备的退避：启动恢复时缺失的模块按这个间隔重试。
const (
	moduleRetryMin = 2 * time.Second
	moduleRetryMax = time.Minute
)

// ---------------------------------------------------------------- 与引擎各入口的衔接

// prepareSnapshot 是目录同步的入口：新快照引用的模块全部就绪才应用快照；任何一个失败就不应用、不记下快照哈希，
// 下一次同步（定时）会重新取得并重试。
func (r *Runtime) prepareSnapshot(ctx context.Context, snapshot CatalogSnapshot) error {
	if r.udf == nil {
		return nil
	}
	refs := refsOfTasks(snapshot.Sets)
	if len(refs) == 0 {
		return nil
	}
	if err := r.udf.EnsureAll(ctx, refs); err != nil {
		return fmt.Errorf("自定义算子模块没有全部就绪，不应用新目录：%w", err)
	}
	return nil
}

// retainModules 在新目录应用之后卸载不再引用的模块与状态。
func (r *Runtime) retainModules() {
	if r.udf == nil {
		return
	}
	keep := map[string]bool{}
	for _, ref := range refsOfTasks(r.catalog.Sets()) {
		keep[ref.hash] = true
	}
	r.udf.Retain(context.Background(), keep)
}

// prepareJobModules 是补算领取的入口：作业绑定的模块备齐后才执行；失败时作业报告失败，不写任何分块。
func (r *Runtime) prepareJobModules(ctx context.Context, set domain.EngineTask) error {
	if r.udf == nil {
		return nil
	}
	return r.udf.EnsureAll(ctx, refsOfTasks([]domain.EngineTask{set}))
}

// loadModulesAtStart 是启动恢复的入口：目录从磁盘恢复后，引用的模块正常情况下已在本地；缺失时按退避拉取，
// 没有就绪的任务由生命周期检查标为不可执行，就绪后恢复。
func (r *Runtime) loadModulesAtStart(ctx context.Context) {
	if r.udf == nil {
		return
	}
	backoff := moduleRetryMin
	for ctx.Err() == nil {
		refs := refsOfTasks(r.catalog.Sets())
		if len(refs) == 0 {
			return
		}
		err := r.udf.EnsureAll(ctx, refs)
		if err == nil {
			return
		}
		log.WarnContextf(ctx, "factor_udf_modules_not_ready error=%v retry_in=%s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, moduleRetryMax)
	}
}

// modulesCheck 是任务的生命周期检查项：引用的自定义算子模块必须就绪。任务不引用自定义算子时没有这一项。
func (r *Runtime) modulesCheck(set domain.EngineTask) (lifecycle.Check, bool) {
	if r.udf == nil {
		return lifecycle.Check{}, false
	}
	refs := refsOfTasks([]domain.EngineTask{set})
	if len(refs) == 0 {
		return lifecycle.Check{}, false
	}
	states := map[string]domain.UDFModuleStatus{}
	for _, s := range r.udf.Status() {
		states[s.Hash] = s
	}
	for _, ref := range refs {
		if r.udf.Ready(ref.hash) {
			continue
		}
		detail := "尚未开始拉取"
		if s, ok := states[ref.hash]; ok {
			detail = s.State
			if s.Error != "" {
				detail += "：" + s.Error
			}
		}
		return lifecycle.Check{
			Name: "自定义算子模块", Status: lifecycle.CheckFail,
			Message: fmt.Sprintf("udf.%s@%.12s 没有就绪（%s）", ref.name, ref.hash, detail),
			Remedy:  "确认管理端可达（引擎按退避自动拉取），或在管理端检查该版本是否存在",
		}, true
	}
	return lifecycle.Check{Name: "自定义算子模块", Status: lifecycle.CheckPass}, true
}

// observeUDF 记录一批自定义算子调用的指标。
func (r *Runtime) observeUDF(udf string, elapsed time.Duration, copied int64, reason string) {
	if r.metrics == nil {
		return
	}
	r.metrics.UDFCallSeconds.WithLabelValues(udf).Observe(elapsed.Seconds())
	r.metrics.UDFCopyBytes.WithLabelValues(udf).Add(float64(copied))
	if reason != "" {
		r.metrics.UDFFailures.WithLabelValues(udf, reason).Inc()
	}
}

// reportUDFMetrics 把各版本模块的状态写进指标（每个版本一个时间序列，当前状态为 1）。
func (r *Runtime) reportUDFMetrics() {
	if r.udf == nil || r.metrics == nil {
		return
	}
	r.metrics.UDFModuleState.Reset()
	for _, s := range r.udf.Status() {
		r.metrics.UDFModuleState.WithLabelValues(s.Name, s.Hash[:min(len(s.Hash), 12)], s.State).Set(1)
	}
}
