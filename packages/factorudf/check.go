package factorudf

import (
	"context"
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// 导出名（设计 7.3）。
const (
	exportMemory  = "memory"
	exportABI     = "moox_udf_abi"
	exportReserve = "moox_udf_reserve"
	exportEval    = "moox_udf_eval"
)

// ModuleInfo 是模块检查通过后得到的信息。
type ModuleInfo struct {
	Hash        string
	Size        int
	Manifest    *Manifest
	InitialPage uint32
}

// 检查失败的错误类型：调用方可以据此区分“模块不合格”与内部错误。
type CheckError struct{ Reason string }

func (e *CheckError) Error() string { return "模块检查未通过：" + e.Reason }

func checkFail(format string, args ...any) error {
	return &CheckError{Reason: fmt.Sprintf(format, args...)}
}

// newRuntimeConfig 是宿主统一使用的运行时配置：编译模式、只开 wasm 2.0 的核心特性（线程、relaxed-simd、memory64、
// 尾调用、异常、GC 等一律不开，用到的模块编译即失败）、guest 调用可被上下文取消中断、实例内存有页数上限。
func newRuntimeConfig(l Limits, cache wazero.CompilationCache) wazero.RuntimeConfig {
	cfg := wazero.NewRuntimeConfigCompiler().
		WithCoreFeatures(api.CoreFeaturesV2).
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(l.MemoryLimitPages)
	if cache != nil {
		cfg = cfg.WithCompilationCache(cache)
	}
	return cfg
}

// Inspect 对 wasm 做静态模块检查（设计 8.2），不执行任何代码：
// 大小、没有导入、没有 start 函数、恰好一个不共享的 32 位内存且初始页数不超限、表不超过一个且元素数不超限、
// 元数据存在且合法。导出签名与 ABI 版本需要编译或实例化，由 Runtime.Load 继续检查。
func Inspect(wasm []byte, l Limits) (*ModuleInfo, error) {
	l = l.WithDefaults()
	if len(wasm) == 0 {
		return nil, checkFail("模块是空的")
	}
	if len(wasm) > l.MaxModuleBytes {
		return nil, checkFail("模块 %d 字节，超过上限 %d", len(wasm), l.MaxModuleBytes)
	}
	info, err := scanWasm(wasm)
	if err != nil {
		return nil, checkFail("%v", err)
	}
	if info.imports > 0 {
		return nil, checkFail("模块有 %d 个导入：UDF 必须是没有任何导入的纯计算", info.imports)
	}
	if info.hasStart {
		return nil, checkFail("模块有 start 函数：实例化时不允许执行任何用户代码")
	}
	switch len(info.memories) {
	case 0:
		return nil, checkFail("模块没有定义内存")
	case 1:
	default:
		return nil, checkFail("模块有 %d 个内存，要恰好一个", len(info.memories))
	}
	mem := info.memories[0]
	switch {
	case mem.shared:
		return nil, checkFail("不允许共享内存（线程特性）")
	case mem.memory64:
		return nil, checkFail("不允许 64 位内存")
	case mem.min > uint64(l.MaxInitialPages):
		return nil, checkFail("初始内存 %d 页，超过上限 %d 页", mem.min, l.MaxInitialPages)
	}
	if len(info.tables) > 1 {
		return nil, checkFail("模块有 %d 个表，最多一个", len(info.tables))
	}
	for _, t := range info.tables {
		// wazero 的表是宿主的 Go 切片，table.grow 既不受线性内存页数限制也不计入预算：必须声明最大值，并且最大值不超过上限。
		if !t.hasMax {
			return nil, checkFail("表没有声明最大元素数：table.grow 会不受限制地扩张")
		}
		if t.max > uint64(l.MaxTableElems) || t.min > uint64(l.MaxTableElems) {
			return nil, checkFail("表声明了 %d～%d 个元素，超过上限 %d", t.min, t.max, l.MaxTableElems)
		}
	}
	if n := info.customCount[CustomSection]; n != 1 {
		if n == 0 {
			return nil, checkFail("缺少自定义段 %s（元数据）", CustomSection)
		}
		return nil, checkFail("自定义段 %s 出现了 %d 次", CustomSection, n)
	}
	manifest, err := ParseManifest(info.custom[CustomSection])
	if err != nil {
		return nil, checkFail("元数据不合格：%v", err)
	}
	return &ModuleInfo{Hash: Hash(wasm), Size: len(wasm), Manifest: manifest, InitialPage: uint32(mem.min)}, nil
}

// checkExports 比对导出签名：memory、moox_udf_abi () -> i32、moox_udf_reserve (i32) -> i32、
// moox_udf_eval (i32 × 6) -> i32，必须完全一致。
func checkExports(compiled wazero.CompiledModule) error {
	want := map[string]struct{ params, results int }{
		exportABI: {0, 1}, exportReserve: {1, 1}, exportEval: {6, 1},
	}
	funcs := compiled.ExportedFunctions()
	for name, sig := range want {
		def, ok := funcs[name]
		if !ok {
			return checkFail("缺少导出函数 %s", name)
		}
		params, results := def.ParamTypes(), def.ResultTypes()
		if len(params) != sig.params || len(results) != sig.results {
			return checkFail("导出函数 %s 的签名不对：参数 %d 个、结果 %d 个，要求参数 %d 个、结果 %d 个", name, len(params), len(results), sig.params, sig.results)
		}
		for _, t := range append(append([]api.ValueType{}, params...), results...) {
			if t != api.ValueTypeI32 {
				return checkFail("导出函数 %s 的参数与结果都要是 i32", name)
			}
		}
	}
	if _, ok := compiled.ExportedMemories()[exportMemory]; !ok {
		return checkFail("缺少导出内存 %s", exportMemory)
	}
	return nil
}

// checkABI 实例化并调用 moox_udf_abi，确认 ABI 版本。
func checkABI(ctx context.Context, rt wazero.Runtime, compiled wazero.CompiledModule, l Limits, budget Budget, initialPages uint32) error {
	meter := newMeter(budget)
	defer meter.release()
	if err := meter.precharge(int64(initialPages) << 16); err != nil {
		return fmt.Errorf("%w：检查模块时申请内存被拒绝：%v", ErrBudget, err)
	}
	cctx, cancel := context.WithTimeout(meter.context(ctx), l.CallTimeout)
	defer cancel()
	mod, err := rt.InstantiateModule(cctx, compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		if meter.Denied() {
			return fmt.Errorf("%w：检查模块时申请内存被拒绝", ErrBudget)
		}
		return checkFail("实例化失败：%v", err)
	}
	defer mod.Close(ctx)
	fn := mod.ExportedFunction(exportABI)
	if fn == nil {
		return checkFail("缺少导出函数 %s", exportABI)
	}
	res, err := fn.Call(cctx)
	if err != nil {
		return checkFail("调用 %s 失败：%v", exportABI, err)
	}
	if got := int32(res[0]); got != ABIVersion {
		return checkFail("ABI 版本 %d 不被支持（当前 %d）", got, ABIVersion)
	}
	return nil
}

var errClosed = errors.New("运行时已关闭")
