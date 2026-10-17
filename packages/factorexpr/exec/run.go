package exec

import (
	"context"
	"fmt"
	"runtime/debug"
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
}

// smallWork 以下的节点不拆分并行，免得调度开销超过计算本身。
const smallWork = 4096

// values 是一个节点在需求区间上的值：序列 s 在网格下标 t 的值位于 data[s*stride+t-start]；常量按标量广播。
type values struct {
	constant bool
	value    float64
	data     []float64
	stride   int
	start    int
}

func (v values) at(series, t int) float64 {
	if v.constant {
		return v.value
	}
	return v.data[series*v.stride+t-v.start]
}

func (v values) series(index int) ops.Series {
	return ops.Series{Data: v.data[index*v.stride : (index+1)*v.stride], Start: v.start}
}

// Run 在面板上计算程序在目标区间上的全部输出。
func Run(ctx context.Context, program *factorexpr.Program, panel *Panel, opts Options) (result *Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = nil, fmt.Errorf("因子计算内部错误：%v\n%s", recovered, debug.Stack())
		}
	}()
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	need, err := program.Need(opts.Start, opts.End)
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
	run := &runner{program: program, panel: panel, need: need, workers: opts.Workers, pool: newPool(),
		nodes: make([]values, len(program.Nodes)), owned: make([]bool, len(program.Nodes))}
	for index, node := range program.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !need[index].Empty() || node.Op == factorexpr.OpConst {
			if err := run.compute(index, node); err != nil {
				return nil, err
			}
		}
		for _, arg := range node.Args {
			if program.Nodes[arg].LastUse == index && run.owned[arg] {
				run.pool.put(run.nodes[arg].data)
				run.owned[arg] = false
				run.nodes[arg] = values{}
			}
		}
	}
	return run.result(opts.Start, opts.End), nil
}

type runner struct {
	program *factorexpr.Program
	panel   *Panel
	need    []factorexpr.Interval
	workers int
	pool    *pool
	nodes   []values
	owned   []bool
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
	}
	interval := r.need[index]
	length := interval.Len()
	seriesCount := len(r.panel.series)
	out := values{data: r.pool.get(seriesCount * length), stride: length, start: interval.Start}
	r.nodes[index], r.owned[index] = out, true
	args := make([]values, len(node.Args))
	for i, arg := range node.Args {
		args[i] = r.nodes[arg]
	}
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
		prepare := ops.RollingKernel(node.Op)
		if prepare == nil {
			return fmt.Errorf("算子 %s 没有时序内核", node.Op)
		}
		kernel := prepare(node.Ints, node.Floats)
		return r.parallel(seriesCount, length, func(lo, hi int) {
			in := make([]ops.Series, len(args))
			for s := lo; s < hi; s++ {
				for k, arg := range args {
					in[k] = arg.series(s)
				}
				kernel(out.data[s*length:(s+1)*length], interval.Start, in)
			}
		})
	case factorexpr.CategoryCrossSection:
		kernel := ops.CrossKernel(node.Op)
		if kernel == nil {
			return fmt.Errorf("算子 %s 没有截面内核", node.Op)
		}
		arg := args[0]
		return r.parallel(length, seriesCount, func(lo, hi int) {
			var input, output []float64
			for i := lo; i < hi; i++ {
				t := interval.Start + i
				for _, group := range r.panel.groups {
					input, output = input[:0], output[:0]
					for _, s := range group {
						input = append(input, arg.at(s, t))
						output = append(output, 0)
					}
					kernel(input, output)
					for k, s := range group {
						out.data[s*length+i] = output[k]
					}
				}
			}
		})
	}
	return fmt.Errorf("算子 %s 的类别未知", node.Op)
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
