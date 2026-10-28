package factorudf

import (
	"errors"
	"fmt"
	"sort"
)

// SeriesCall 是一条序列的一次调用：各输入在 [起点, 起点+InLen) 的窗口、参数值、要写的输出区间。
type SeriesCall struct {
	// Key 由调用方使用（例如标的序号），分批不解释它。
	Key    int
	Params []float64
	// L 是这条序列在这组参数下的窗口长度（由 Manifest.LookbackFor 得到）；L 不同的序列不能混批。
	L int64
}

// Group 是一批可以合并到同一次 guest 调用的序列：窗口长度相同。
type Group struct {
	L      int64
	Series []int // 在输入切片里的位置，保持原有顺序
}

// GroupByL 按窗口长度分组：L 不同不混批；组按 L 升序，组内保持输入顺序，结果与输入的并发度无关。
func GroupByL(calls []SeriesCall) []Group {
	byL := map[int64][]int{}
	for i, c := range calls {
		byL[c.L] = append(byL[c.L], i)
	}
	groups := make([]Group, 0, len(byL))
	for l, idx := range byL {
		groups = append(groups, Group{L: l, Series: idx})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].L < groups[j].L })
	return groups
}

// BatchCapacity 返回每批最多放多少条序列：
//
//	floor(批字节上限 / (8 × (输入个数 × inLen + 参数个数 + outLen)))
//
// 计算过程检查溢出；结果为 0（单条序列都放不下）时返回错误，提示调整批字节上限。
func BatchCapacity(batchBytes int64, nInputs, nParams, inLen, outLen int) (int, error) {
	if batchBytes <= 0 || nInputs < 1 || nParams < 0 || inLen < 1 || outLen < 1 {
		return 0, errors.New("批容量的参数不合法")
	}
	const limit = int64(1) << 50
	perSeries := int64(nInputs)
	if perSeries > limit/int64(inLen) {
		return 0, errors.New("批容量计算溢出")
	}
	perSeries = perSeries*int64(inLen) + int64(nParams) + int64(outLen)
	if perSeries > limit/8 {
		return 0, errors.New("批容量计算溢出")
	}
	perSeries *= 8
	n := batchBytes / perSeries
	if n < 1 {
		return 0, fmt.Errorf("单条序列需要 %d 字节，超过批字节上限 %d：缩短回看或调大 udf 批字节上限", perSeries, batchBytes)
	}
	if n > int64(1<<31-1) {
		n = 1<<31 - 1
	}
	return int(n), nil
}

// SplitBatches 把一组序列按批容量拆成若干批（保持顺序）。
func SplitBatches(series []int, capacity int) [][]int {
	if capacity < 1 {
		return nil
	}
	var out [][]int
	for len(series) > 0 {
		n := min(capacity, len(series))
		out = append(out, series[:n])
		series = series[n:]
	}
	return out
}
