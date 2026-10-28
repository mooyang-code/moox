// Package udfhost 把 packages/factorudf（wazero 宿主）接到因子表达式引擎：实现 exec.UDFHost，并把模块元数据转换成
// factorexpr 的自定义算子解析环境。FactorMgr（试算）与 FactorEngine（计算）共用。
package udfhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
	"github.com/mooyang-code/moox/packages/factorudf"
)

// Host 持有已加载的模块（按版本哈希），执行 exec 交来的调用。
type Host struct {
	rt *factorudf.Runtime

	mu      sync.RWMutex
	modules map[string]*factorudf.Module
	// loadMu 串行化 Ensure 的加载，避免同一版本被并发地拉取与编译。
	loadMu sync.Mutex
	// observe 在每批调用结束后调用（耗时、拷进模块的字节数、失败原因；成功时 reason 为空），可为 nil。
	observe func(udf string, elapsed time.Duration, copied int64, reason string)
}

// SetObserver 设置调用观察者（指标）；必须在开始执行之前调用。
func (h *Host) SetObserver(observe func(udf string, elapsed time.Duration, copied int64, reason string)) {
	h.observe = observe
}

// New 创建宿主；运行时由调用方创建并负责关闭。
func New(rt *factorudf.Runtime) *Host {
	return &Host{rt: rt, modules: map[string]*factorudf.Module{}}
}

// Runtime 返回底层运行时。
func (h *Host) Runtime() *factorudf.Runtime { return h.rt }

// Load 加载一个版本的 wasm（完整模块检查后注册）；同一哈希重复加载返回已有的。
func (h *Host) Load(ctx context.Context, wasm []byte) (*factorudf.Module, error) {
	m, err := h.rt.Load(ctx, wasm)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.modules[m.Info.Hash] = m
	h.mu.Unlock()
	return m, nil
}

// Ensure 确保版本已加载：没有就用 fetch 取字节并加载（完整模块检查），并核对字节的哈希与要求的一致。
// 同一时刻只加载一次（串行），已加载的直接返回。
func (h *Host) Ensure(ctx context.Context, hash string, fetch func(ctx context.Context, hash string) ([]byte, error)) error {
	if h.Has(hash) {
		return nil
	}
	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	if h.Has(hash) {
		return nil
	}
	wasm, err := fetch(ctx, hash)
	if err != nil {
		return fmt.Errorf("取自定义算子版本 %s：%w", short(hash), err)
	}
	if got := factorudf.Hash(wasm); got != hash {
		return fmt.Errorf("自定义算子版本 %s 的字节哈希是 %s", short(hash), short(got))
	}
	_, err = h.Load(ctx, wasm)
	return err
}

// Module 返回已加载的版本。
func (h *Host) Module(hash string) (*factorudf.Module, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.modules[hash]
	return m, ok
}

// Has 报告版本是否已加载（模块就绪）。
func (h *Host) Has(hash string) bool {
	_, ok := h.Module(hash)
	return ok
}

// Unload 卸载不再被任何目录引用的版本。
func (h *Host) Unload(ctx context.Context, hash string) {
	h.mu.Lock()
	delete(h.modules, hash)
	h.mu.Unlock()
	h.rt.Unload(ctx, hash)
}

// Hashes 返回已加载的版本。
func (h *Host) Hashes() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.modules))
	for hash := range h.modules {
		out = append(out, hash)
	}
	return out
}

// Eval 实现 exec.UDFHost。
func (h *Host) Eval(ctx context.Context, call *exec.UDFCall) error {
	m, ok := h.Module(call.Hash)
	if !ok {
		return fmt.Errorf("自定义算子版本 %s 的模块没有加载", short(call.Hash))
	}
	started := time.Now()
	err := m.Eval(ctx, &factorudf.Batch{
		NSeries: call.NSeries, InLen: call.InLen, OutLen: call.OutLen,
		Inputs: call.Inputs, Params: call.Params, Out: call.Out,
	})
	if h.observe != nil {
		copied := int64(len(call.Params)) * 8
		for _, in := range call.Inputs {
			copied += int64(len(in)) * 8
		}
		h.observe(m.Info.Manifest.Name, time.Since(started), copied, failureReason(err))
	}
	return err
}

// failureReason 把调用错误归成指标用的原因；成功为空串。
func failureReason(err error) string {
	if err == nil {
		return ""
	}
	var ce *factorudf.CallError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	if errors.Is(err, factorudf.ErrBudget) {
		return factorudf.ReasonBudget
	}
	return "other"
}

// BatchBytes 实现 exec.UDFHost。
func (h *Host) BatchBytes() int64 { return int64(h.rt.Limits().BatchBytes) }

var _ exec.UDFHost = (*Host)(nil)

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// Describe 是一个版本的说明（来自包内 manifest.json，不进 wasm、不进版本哈希）。
type Describe struct {
	Doc     string
	Example string
}

// UDFFromManifest 把模块元数据转换成 factorexpr 的自定义算子定义。
func UDFFromManifest(m *factorudf.Manifest, hash string, d Describe) (factorexpr.UDF, error) {
	lb, err := factorudf.ParseLookback(m.Lookback, m.Params)
	if err != nil {
		return factorexpr.UDF{}, err
	}
	u := factorexpr.UDF{
		Name: m.Name, Hash: hash, Inputs: append([]string(nil), m.Inputs...),
		LookbackA: lb.A, LookbackB: lb.B, LookbackParam: lb.Param,
		Relaxed: m.NaN == factorudf.NaNRelaxed, MinValid: m.MinValid, Doc: d.Doc, Example: d.Example,
	}
	if lb.Param == "" {
		u.LookbackA = 0
	}
	for _, p := range m.Params {
		u.Params = append(u.Params, factorexpr.UDFParam{Name: p.Name, Int: p.Type == factorudf.ParamInt, Min: p.Min, Max: p.Max})
	}
	return u, nil
}

// Budget 是一个固定上限的内存预算（factorudf.Budget 的简单实现）：管理端的发布复核与试算各用一个，与引擎分开计算。
type Budget struct {
	mu    sync.Mutex
	limit int64
	used  int64
}

// NewBudget 创建上限为 limit 字节的预算。
func NewBudget(limit int64) *Budget { return &Budget{limit: limit} }

// Acquire 申请 bytes 字节；超过上限拒绝。
func (b *Budget) Acquire(bytes int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+bytes > b.limit {
		return fmt.Errorf("需要 %d 字节，已用 %d、上限 %d", bytes, b.used, b.limit)
	}
	b.used += bytes
	return nil
}

// Release 归还 bytes 字节。
func (b *Budget) Release(bytes int64) {
	b.mu.Lock()
	b.used = max(b.used-bytes, 0)
	b.mu.Unlock()
}

// Used 返回已用字节数（指标与测试用）。
func (b *Budget) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

var _ factorudf.Budget = (*Budget)(nil)
