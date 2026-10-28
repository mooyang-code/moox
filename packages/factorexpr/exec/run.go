package exec

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/ops"
)

// Options 是一次执行的参数。
type Options struct {
	// Start、End 是目标网格下标（闭区间）：实时周期只有最后一个下标，补算是一个分块。
	Start, End int
	// Workers 是并行的 goroutine 数，小于 1 时按 1。结果与它无关。
	Workers int
	// Params 是参数槽（factorexpr.ParamSlot）的取值：每条序列一个值，按 Panel.Series 的顺序，在整个计算区间上不变
	// （目标期的快照覆盖整个回看区间）。没有给出的参数槽用默认值。
	Params map[string][]float64
	// Universes 是标的范围标识到序列成员掩码的映射（按 Panel.Series 的顺序）：截面算子只在掩码为真的序列之间计算，
	// 范围外的序列结果为 NaN。没有给出的范围（包括空串）表示全部序列。
	Universes map[string][]bool
	// UDF 是自定义算子的执行宿主；程序里没有自定义算子时不需要。
	UDF UDFHost
}

// smallWork 以下的节点不拆分并行，免得调度开销超过计算本身。
const smallWork = 4096

// values 是一个节点的值。三种形态：常量按标量广播；scalars 非空时每条序列一个值、不随时间变化（参数与只由参数决定的节点）；
// 否则是需求区间上的序列，序列 s 在网格下标 t 的值位于 data[s*stride+t-start]。
type values struct {
	constant bool
	value    float64
	scalars  []float64
	data     []float64
	stride   int
	start    int
}

func (v values) at(series, t int) float64 {
	if v.constant {
		return v.value
	}
	if v.scalars != nil {
		return v.scalars[series]
	}
	return v.data[series*v.stride+t-v.start]
}

func (v values) series(index int) ops.Series {
	return ops.Series{Data: v.data[index*v.stride : (index+1)*v.stride], Start: v.start}
}

// Run 在面板上计算程序在目标区间上的全部输出。
//
// 执行前按本次的参数取值去重：结构相同、参数槽取值相同（截面依赖节点还要求标的范围相同）的节点只算一次。
// 程序结构只编译一次，这一步只是遍历节点，开销很小。
func Run(ctx context.Context, program *factorexpr.Program, panel *Panel, opts Options) (result *Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = nil, fmt.Errorf("因子计算内部错误：%v\n%s", recovered, debug.Stack())
		}
	}()
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	params, err := bindParams(program, panel, opts.Params)
	if err != nil {
		return nil, err
	}
	need, err := program.NeedWith(opts.Start, opts.End, func(node *factorexpr.ProgramNode) (int, int) {
		return actualExtent(node, params, len(panel.series))
	})
	if err != nil {
		return nil, err
	}
	if err := checkNeed(program, need, panel.length); err != nil {
		return nil, err
	}
	for _, column := range program.Columns {
		if _, ok := panel.colIndex[column]; !ok {
			return nil, fmt.Errorf("面板缺少输入列 %s", column)
		}
	}
	universes, err := bindUniverses(program, panel, opts.Universes)
	if err != nil {
		return nil, err
	}
	alias, lastUse, need := dedup(program, params, universes, need)
	run := &runner{ctx: ctx, udf: opts.UDF, program: program, panel: panel, need: need, workers: opts.Workers, pool: newPool(), params: params,
		universes: universes, alias: alias, nodes: make([]values, len(program.Nodes)), owned: make([]bool, len(program.Nodes))}
	for index, node := range program.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if alias[index] == index && (!need[index].Empty() || node.Op == factorexpr.OpConst || node.Scalar) {
			if err := run.compute(index, node); err != nil {
				return nil, err
			}
		}
		for _, arg := range node.Args {
			canonical := alias[arg]
			if lastUse[canonical] == index && run.owned[canonical] {
				run.pool.put(run.nodes[canonical].data)
				run.owned[canonical] = false
				run.nodes[canonical] = values{}
			}
		}
	}
	return run.result(opts.Start, opts.End), nil
}

// actualExtent 返回节点在本次参数取值下的实际扩展：各序列中最大的 back 与最小的 shift。没有参数槽的节点就是编译期的值。
func actualExtent(node *factorexpr.ProgramNode, params map[string][]float64, seriesCount int) (int, int) {
	if !node.HasParamSlots() || node.Function == nil {
		return node.Back, node.Shift
	}
	if seriesCount == 0 {
		return node.Back, node.Shift
	}
	back, shift := 0, math.MaxInt
	ints := make([]int, len(node.Ints))
	uniform := true
	for _, slot := range node.IntParams {
		if slot != "" && !uniformVector(params[slot]) {
			uniform = false
		}
	}
	if uniform {
		// 各序列取值相同（默认值、统一值）：算一次即可。
		seriesCount = 1
	}
	for s := 0; s < seriesCount; s++ {
		for i, value := range node.Ints {
			if slot := node.IntParams[i]; slot != "" {
				value = int(params[slot][s])
			}
			ints[i] = value
		}
		back, shift = max(back, node.Function.Back(ints)), min(shift, node.Function.Shift(ints))
	}
	return min(back, node.Back), max(shift, node.Shift)
}

// bindParams 返回每个参数槽在每条序列上的取值；没有给出的参数槽用默认值。
func bindParams(program *factorexpr.Program, panel *Panel, given map[string][]float64) (map[string][]float64, error) {
	bound := make(map[string][]float64, len(program.Params))
	count := len(panel.series)
	for _, param := range program.Params {
		vector, ok := given[param.Slot]
		if !ok {
			vector = make([]float64, count)
			for i := range vector {
				vector[i] = param.Default
			}
		}
		if len(vector) != count {
			return nil, fmt.Errorf("参数 %s 的取值有 %d 个，面板有 %d 条序列", param.Slot, len(vector), count)
		}
		bound[param.Slot] = vector
	}
	return bound, nil
}

func bindUniverses(program *factorexpr.Program, panel *Panel, given map[string][]bool) (map[string][]bool, error) {
	bound := map[string][]bool{}
	for _, node := range program.Nodes {
		if !node.Cross || node.Universe == "" {
			continue
		}
		mask, ok := given[node.Universe]
		if !ok {
			continue
		}
		if len(mask) != len(panel.series) {
			return nil, fmt.Errorf("标的范围 %s 的掩码有 %d 项，面板有 %d 条序列", node.Universe, len(mask), len(panel.series))
		}
		bound[node.Universe] = mask
	}
	return bound, nil
}

// dedup 按本次的取值给节点去重，返回每个节点对应的计算节点（alias[i] == i 表示自己计算）、计算节点的最后使用者，
// 以及合并后的需求区间（计算节点取它与全部同值节点需求的并集）。
func dedup(program *factorexpr.Program, params map[string][]float64, universes map[string][]bool,
	need []factorexpr.Interval) ([]int, []int, []factorexpr.Interval) {
	digests := make(map[string]string, len(params))
	for slot, vector := range params {
		digests[slot] = digestFloats(vector)
	}
	universeDigests := make(map[string]string, len(universes))
	for name, mask := range universes {
		universeDigests[name] = digestBools(mask)
	}
	alias := make([]int, len(program.Nodes))
	lastUse := make([]int, len(program.Nodes))
	merged := append([]factorexpr.Interval(nil), need...)
	seen := make(map[string]int, len(program.Nodes))
	for index, node := range program.Nodes {
		key := runtimeKey(node, alias, digests, universeDigests)
		canonical, ok := seen[key]
		if !ok {
			canonical = index
			seen[key] = index
		}
		alias[index] = canonical
		lastUse[canonical] = max(lastUse[canonical], node.LastUse)
		if canonical != index {
			merged[canonical] = union(merged[canonical], need[index])
		}
	}
	return alias, lastUse, merged
}

func union(a, b factorexpr.Interval) factorexpr.Interval {
	switch {
	case a.Empty():
		return b
	case b.Empty():
		return a
	}
	return factorexpr.Interval{Start: min(a.Start, b.Start), End: max(a.End, b.End)}
}

// runtimeKey 与 programKey 相同，只是参数槽换成本次取值的摘要、子节点换成去重后的计算节点、标的范围换成掩码的摘要。
func runtimeKey(node *factorexpr.ProgramNode, alias []int, digests, universes map[string]string) string {
	var b strings.Builder
	b.WriteString(node.Op)
	switch node.Op {
	case factorexpr.OpColumn:
		b.WriteString(":" + node.Column)
	case factorexpr.OpConst:
		b.WriteString(":" + strconv.Itoa(int(node.Kind)) + ":" + strconv.FormatUint(math.Float64bits(node.Value), 16))
	case factorexpr.OpParam:
		b.WriteString(":" + digests[node.Param])
	}
	b.WriteByte('(')
	for _, arg := range node.Args {
		b.WriteString(strconv.Itoa(alias[arg]) + ",")
	}
	b.WriteByte(';')
	for i, value := range node.Ints {
		if slot := node.IntParams[i]; slot != "" {
			b.WriteString("$" + digests[slot] + ",")
			continue
		}
		b.WriteString(strconv.Itoa(value) + ",")
	}
	b.WriteByte(';')
	for i, value := range node.Floats {
		if slot := node.FloatParams[i]; slot != "" {
			b.WriteString("$" + digests[slot] + ",")
			continue
		}
		b.WriteString(strconv.FormatUint(math.Float64bits(value), 16) + ",")
	}
	if node.Cross {
		universe, ok := universes[node.Universe]
		if !ok {
			universe = "all"
		}
		b.WriteString(";u=" + universe)
	}
	b.WriteByte(')')
	return b.String()
}

func uniformVector(vector []float64) bool {
	for _, value := range vector {
		if math.Float64bits(value) != math.Float64bits(vector[0]) {
			return false
		}
	}
	return true
}

// digestFloats 返回取值向量的摘要。所有序列取值相同（默认值、统一值，最常见）时直接用这个值，不做哈希。
func digestFloats(vector []float64) string {
	if len(vector) > 0 && uniformVector(vector) {
		return "c" + strconv.FormatUint(math.Float64bits(vector[0]), 16)
	}
	hash := sha256.New()
	var buffer [8]byte
	for _, value := range vector {
		binary.LittleEndian.PutUint64(buffer[:], math.Float64bits(value))
		hash.Write(buffer[:])
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func digestBools(mask []bool) string {
	hash := sha256.New()
	for _, member := range mask {
		if member {
			hash.Write([]byte{1})
		} else {
			hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

type runner struct {
	ctx       context.Context
	udf       UDFHost
	bigBatch  sync.Mutex // 一条序列就超过 worker 份额的自定义算子批一次只跑一个
	program   *factorexpr.Program
	panel     *Panel
	need      []factorexpr.Interval
	workers   int
	pool      *pool
	params    map[string][]float64
	universes map[string][]bool
	alias     []int
	nodes     []values
	owned     []bool
}

func (r *runner) compute(index int, node *factorexpr.ProgramNode) error {
	switch node.Op {
	case factorexpr.OpConst:
		r.nodes[index] = values{constant: true, value: node.Value}
		return nil
	case factorexpr.OpColumn:
		column := r.panel.colIndex[node.Column]
		r.nodes[index] = values{data: r.panel.data[column], stride: r.panel.length}
		return nil
	case factorexpr.OpParam:
		r.nodes[index] = values{scalars: r.params[node.Param]}
		return nil
	}
	seriesCount := len(r.panel.series)
	args := make([]values, len(node.Args))
	for i, arg := range node.Args {
		args[i] = r.nodes[r.alias[arg]]
	}
	if node.Scalar {
		scalar := factorexpr.Scalar(node.Op)
		if scalar == nil {
			return fmt.Errorf("算子 %s 没有逐元素实现", node.Op)
		}
		out := make([]float64, seriesCount)
		buffer := make([]float64, len(args))
		for s := range out {
			for k, arg := range args {
				buffer[k] = arg.at(s, 0)
			}
			out[s] = scalar(buffer)
		}
		r.nodes[index] = values{scalars: out}
		return nil
	}
	interval := r.need[index]
	length := interval.Len()
	out := values{data: r.pool.get(seriesCount * length), stride: length, start: interval.Start}
	r.nodes[index], r.owned[index] = out, true
	switch node.Function.Category {
	case factorexpr.CategoryElementwise:
		scalar := factorexpr.Scalar(node.Op)
		if scalar == nil {
			return fmt.Errorf("算子 %s 没有逐元素实现", node.Op)
		}
		return r.parallel(seriesCount, length, func(lo, hi int) {
			buffer := make([]float64, len(args))
			for s := lo; s < hi; s++ {
				row := out.data[s*length : (s+1)*length]
				for i := range row {
					t := interval.Start + i
					for k, arg := range args {
						buffer[k] = arg.at(s, t)
					}
					row[i] = scalar(buffer)
				}
			}
		})
	case factorexpr.CategoryTimeSeries:
		if udf := node.Function.UDF(); udf != nil {
			return r.computeUDF(node, udf, args, out, interval)
		}
		prepare := ops.RollingKernel(node.Op)
		if prepare == nil {
			return fmt.Errorf("算子 %s 没有时序内核", node.Op)
		}
		kernels, bySeries := r.seriesKernels(node, prepare)
		// 每条序列的工作量是输出行数加上窗口向前扩展的根数：实时周期只输出 1 行，但 ema(x, n, 50000) 这类节点每行要扫 5 万根，
		// 只按输出行数估算会让它永远单线程执行。
		return r.parallel(seriesCount, length+node.Function.Back(node.Ints), func(lo, hi int) {
			in := make([]ops.Series, len(args))
			for s := lo; s < hi; s++ {
				row := out.data[s*length : (s+1)*length]
				kernel := kernels[bySeries[s]]
				if kernel == nil {
					for i := range row {
						row[i] = math.NaN()
					}
					continue
				}
				for k, arg := range args {
					in[k] = arg.series(s)
				}
				kernel(row, interval.Start, in)
			}
		})
	case factorexpr.CategoryCrossSection:
		kernel := ops.CrossKernel(node.Op)
		if kernel == nil {
			return fmt.Errorf("算子 %s 没有截面内核", node.Op)
		}
		arg := args[0]
		mask := r.universes[node.Universe]
		return r.parallel(length, seriesCount, func(lo, hi int) {
			var input, output []float64
			for i := lo; i < hi; i++ {
				t := interval.Start + i
				for _, group := range r.panel.groups {
					input, output = input[:0], output[:0]
					for _, s := range group {
						if mask != nil && !mask[s] {
							out.data[s*length+i] = math.NaN()
							continue
						}
						input = append(input, arg.at(s, t))
						output = append(output, 0)
					}
					kernel(input, output)
					k := 0
					for _, s := range group {
						if mask != nil && !mask[s] {
							continue
						}
						out.data[s*length+i] = output[k]
						k++
					}
				}
			}
		})
	}
	return fmt.Errorf("算子 %s 的类别未知", node.Op)
}

// seriesKernels 为时序节点准备内核。没有参数槽时所有序列共用一个内核；有参数槽时按每条序列的具体取值分组，
// 每组准备一次内核（与常量参数完全同一套内核，结果逐位相同）。取值不满足算子联合约束的序列没有内核，结果为 NaN
// （提交参数时已经拒绝这类取值，这里只是兜底）。
func (r *runner) seriesKernels(node *factorexpr.ProgramNode, prepare ops.Rolling) ([]ops.SeriesKernel, []int) {
	seriesCount := len(r.panel.series)
	bySeries := make([]int, seriesCount)
	if !node.HasParamSlots() {
		return []ops.SeriesKernel{prepare(node.Ints, node.Floats)}, bySeries
	}
	var kernels []ops.SeriesKernel
	groups := map[string]int{}
	ints := make([]int, len(node.Ints))
	floats := make([]float64, len(node.Floats))
	for s := 0; s < seriesCount; s++ {
		var key strings.Builder
		for i, value := range node.Ints {
			if slot := node.IntParams[i]; slot != "" {
				value = int(r.params[slot][s])
			}
			ints[i] = value
			key.WriteString(strconv.Itoa(value) + ",")
		}
		key.WriteByte(';')
		for i, value := range node.Floats {
			if slot := node.FloatParams[i]; slot != "" {
				value = r.params[slot][s]
			}
			floats[i] = value
			key.WriteString(strconv.FormatUint(math.Float64bits(value), 16) + ",")
		}
		group, ok := groups[key.String()]
		if !ok {
			group = len(kernels)
			groups[key.String()] = group
			var kernel ops.SeriesKernel
			if node.Function.Validate(ints, floats) == "" {
				kernel = prepare(append([]int(nil), ints...), append([]float64(nil), floats...))
			}
			kernels = append(kernels, kernel)
		}
		bySeries[s] = group
	}
	return kernels, bySeries
}

// parallelErr 与 parallel 相同，只是 fn 可以返回错误（自定义算子执行失败要让整次运行失败）；多个错误取最先返回的一个。
func (r *runner) parallelErr(n, perItem int, fn func(lo, hi int) error) error {
	workers := r.workers
	if workers > n {
		workers = n
	}
	if workers <= 1 || n*perItem < smallWork {
		return fn(0, n)
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	var once sync.Once
	var failure error
	for lo := 0; lo < n; lo += chunk {
		hi := min(n, lo+chunk)
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					once.Do(func() { failure = fmt.Errorf("因子计算内部错误：%v\n%s", recovered, debug.Stack()) })
				}
			}()
			if err := fn(lo, hi); err != nil {
				once.Do(func() { failure = err })
			}
		}(lo, hi)
	}
	wg.Wait()
	return failure
}

// parallel 把 [0, n) 分块交给 workers 个 goroutine；perItem 是每一项的工作量，用来判断是否值得并行。
// 每个输出值只由一个 goroutine 计算，没有跨 goroutine 的归约，所以结果与 workers 无关。goroutine 里的 panic 转成错误。
func (r *runner) parallel(n, perItem int, fn func(lo, hi int)) error {
	workers := r.workers
	if workers > n {
		workers = n
	}
	if workers <= 1 || n*perItem < smallWork {
		fn(0, n)
		return nil
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	var once sync.Once
	var failure error
	for lo := 0; lo < n; lo += chunk {
		hi := min(n, lo+chunk)
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					once.Do(func() { failure = fmt.Errorf("因子计算内部错误：%v\n%s", recovered, debug.Stack()) })
				}
			}()
			fn(lo, hi)
		}(lo, hi)
	}
	wg.Wait()
	return failure
}
