// Package udfhost 把 packages/factorudf（wazero 宿主）接到因子表达式引擎：实现 exec.UDFHost，并把模块元数据转换成
// factorexpr 的自定义算子解析环境。FactorMgr（试算）与 FactorEngine（计算）共用。
package udfhost

import (
	"context"
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
	"github.com/mooyang-code/moox/packages/factorudf"
)

// Host 持有已加载的模块（按版本哈希），执行 exec 交来的调用。
type Host struct {
	rt *factorudf.Runtime

	mu      sync.RWMutex
	modules map[string]*factorudf.Module
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
	return m.Eval(ctx, &factorudf.Batch{
		NSeries: call.NSeries, InLen: call.InLen, OutLen: call.OutLen,
		Inputs: call.Inputs, Params: call.Params, Out: call.Out,
	})
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
