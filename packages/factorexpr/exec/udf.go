package exec

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// UDFCall 是交给宿主的一批自定义算子调用：同一个版本、同一个窗口长度 L 的若干条序列。
//
// 布局都是 [序列][...]：Inputs[k] 是第 k 个输入，长度 NSeries*InLen；Params 长度 NSeries*参数个数（元数据声明顺序，
// 整数参数以 float64 精确传入）；Out 长度 NSeries*OutLen。InLen = OutLen + L − 1，Out[s][j] 对应以
// Inputs[k][s][j+L−1] 结尾的窗口。
type UDFCall struct {
	Hash    string
	NSeries int
	InLen   int
	OutLen  int
	Inputs  [][]float64
	Params  []float64
	Out     []float64
}

// UDFHost 执行自定义算子：由 modules/factor 注入（packages/factorudf 的实现），factorexpr 不依赖 wazero。
type UDFHost interface {
	// Eval 执行一批。失败（trap、超时、内存、预算）返回错误，不得写 NaN：失败要让本次运行整体失败。
	Eval(ctx context.Context, call *UDFCall) error
	// BatchBytes 是单批输入输出的字节预算，决定每批的序列数。
	BatchBytes() int64
}

// DefaultUDFBatchBytes 是没有给出预算时的单批字节上限，也是内存估算里每个 UDF 节点的拷贝缓冲上界。
const DefaultUDFBatchBytes int64 = 64 << 20

// errNoUDFHost 表示程序里有自定义算子但没有提供宿主。
var errNoUDFHost = errors.New("程序引用了自定义算子，但执行环境没有提供宿主（exec.Options.UDF）")

// computeUDF 计算一个自定义算子节点：每条序列按自己的参数取窗口长度 L，同一个 L 的序列分批交给宿主，结果写进输出行。
// 窗口的拷贝、空值规则、逐点循环都在模块里；这里只做取数、分批与回填。宿主返回错误时整次运行失败，输出不写 NaN。
func (r *runner) computeUDF(node *factorexpr.ProgramNode, udf *factorexpr.UDF, args []values, out values, interval factorexpr.Interval) error {
	if r.udf == nil {
		return errNoUDFHost
	}
	seriesCount := len(r.panel.series)
	length := interval.Len()
	isInt, index := udf.ParamSlots()
	return r.parallelErr(seriesCount, length+node.Function.Back(node.Ints), func(lo, hi int) error {
		// 每条序列的参数值（元数据声明顺序）与窗口长度。
		nParams := len(udf.Params)
		params := make([]float64, (hi-lo)*nParams)
		ints := make([]int, len(node.Ints))
		byLen := map[int][]int{}
		for s := lo; s < hi; s++ {
			for i, value := range node.Ints {
				if slot := node.IntParams[i]; slot != "" {
					value = int(r.params[slot][s])
				}
				ints[i] = value
			}
			for p := range udf.Params {
				var value float64
				if isInt[p] {
					value = float64(ints[index[p]])
				} else if slot := node.FloatParams[index[p]]; slot != "" {
					value = r.params[slot][s]
				} else {
					value = node.Floats[index[p]]
				}
				params[(s-lo)*nParams+p] = value
			}
			l := udf.Window(ints)
			byLen[l] = append(byLen[l], s)
		}
		lens := make([]int, 0, len(byLen))
		for l := range byLen {
			lens = append(lens, l)
		}
		sort.Ints(lens)
		budget := r.udf.BatchBytes()
		if budget <= 0 || budget > DefaultUDFBatchBytes {
			budget = DefaultUDFBatchBytes // 内存估算按这个上限计，实际执行不能超过它
		}
		// 批字节预算是整次运行同时在途的拷贝缓冲的上限（内存估算按它计）：并行的 worker 均分它，每个 worker 的批不超过自己的份额。
		// 一条序列放不进份额、但放得进整个预算时（回看很长），这样的批独占（batchGate 写锁，不与任何其他批并行），总量仍不超过预算。
		share := max(budget/int64(max(r.workers, 1)), 1)
		for _, l := range lens {
			group := byLen[l]
			inLen := length + l - 1
			per := (int64(len(args))*int64(inLen) + int64(nParams) + int64(length)) * 8
			if per <= 0 || per > budget {
				return fmt.Errorf("自定义算子 %s：单条序列需要 %d 字节，超过批字节上限 %d（回看 %d）", udf.CallName(), per, budget, l)
			}
			capacity := int(max(share/per, 1))
			oversized := per > share
			for from := 0; from < len(group); from += capacity {
				batch := group[from:min(from+capacity, len(group))]
				if oversized {
					r.batchGate.Lock()
				} else {
					r.batchGate.RLock()
				}
				err := r.udfBatch(udf, args, out, interval, l, batch, params, lo, nParams)
				if oversized {
					r.batchGate.Unlock()
				} else {
					r.batchGate.RUnlock()
				}
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// udfBatch 组装并执行一批：窗口从输入节点的需求区间里取（需求区间按最坏的 L 扩展，覆盖每条序列自己的窗口）。
func (r *runner) udfBatch(udf *factorexpr.UDF, args []values, out values, interval factorexpr.Interval, l int, batch []int,
	allParams []float64, lo, nParams int) error {
	length := interval.Len()
	inLen := length + l - 1
	call := &UDFCall{
		Hash: udf.Hash, NSeries: len(batch), InLen: inLen, OutLen: length,
		Inputs: make([][]float64, len(args)), Params: make([]float64, 0, len(batch)*nParams), Out: make([]float64, len(batch)*length),
	}
	for k, arg := range args {
		flat := make([]float64, len(batch)*inLen)
		for b, s := range batch {
			series := arg.series(s)
			window := flat[b*inLen : (b+1)*inLen]
			from := interval.Start - (l - 1)
			if from < series.Start || from+inLen > series.Start+len(series.Data) {
				return fmt.Errorf("自定义算子 %s：输入窗口 [%d, %d] 超出输入节点计算的区间 [%d, %d]", udf.CallName(), from, from+inLen-1,
					series.Start, series.Start+len(series.Data)-1)
			}
			copy(window, series.Data[from-series.Start:from-series.Start+inLen])
		}
		call.Inputs[k] = flat
	}
	for _, s := range batch {
		call.Params = append(call.Params, allParams[(s-lo)*nParams:(s-lo+1)*nParams]...)
	}
	if err := r.udf.Eval(r.ctx, call); err != nil {
		return fmt.Errorf("自定义算子 %s 执行失败：%w", udf.CallName(), err)
	}
	for b, s := range batch {
		copy(out.data[s*length:(s+1)*length], call.Out[b*length:(b+1)*length])
	}
	return nil
}

// udfCopyBytes 估算一个自定义算子节点每次调用的拷贝缓冲：输入窗口、参数与输出，上界是单批字节预算。
func udfCopyBytes(node *factorexpr.ProgramNode, series, length int) int64 {
	udf := node.Function.UDF()
	if udf == nil {
		return 0
	}
	inLen := int64(length + node.Back)
	per := (int64(len(node.Args))*inLen + int64(len(udf.Params)) + int64(length)) * 8
	total := per * int64(series)
	if total > DefaultUDFBatchBytes || total < 0 {
		return DefaultUDFBatchBytes
	}
	return total
}
