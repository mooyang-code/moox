package exec

// Column 是一个输出列在目标区间上的值：序列 s 在目标下标 t 的值位于 Values[s*Len()+(t−Start)]。
type Column struct {
	FactorID string
	Name     string
	Values   []float64
}

// Result 是一次执行的列式结果（设计 7.4）。只有 Present 为真的位置才对应源行，组装时只输出这些位置。
type Result struct {
	Start, End int
	Series     []SeriesKey
	Columns    []Column
	present    []bool
	// PeakBufferBytes 是执行中同时存活的节点缓冲区峰值，与 EstimateMemory 的 PeakBuffers 一致。
	PeakBufferBytes int64
}

// Len 返回目标区间的长度。
func (r *Result) Len() int { return r.End - r.Start + 1 }

// Present 报告序列 series 在网格下标 t 上是否有源行。
func (r *Result) Present(series, t int) bool { return r.present[series*r.Len()+t-r.Start] }

// Value 返回输出列 column 在序列 series、网格下标 t 上的值。
func (r *Result) Value(column, series, t int) float64 {
	return r.Columns[column].Values[series*r.Len()+t-r.Start]
}

func (r *runner) result(start, end int) *Result {
	length := end - start + 1
	seriesCount := len(r.panel.series)
	result := &Result{Start: start, End: end, Series: r.panel.series, PeakBufferBytes: r.pool.peak,
		present: make([]bool, seriesCount*length)}
	for s := 0; s < seriesCount; s++ {
		copy(result.present[s*length:(s+1)*length], r.panel.present[s*r.panel.length+start:s*r.panel.length+end+1])
	}
	for _, output := range r.program.Outputs {
		node := r.nodes[output.Node]
		column := Column{FactorID: output.FactorID, Name: output.Name, Values: make([]float64, seriesCount*length)}
		for s := 0; s < seriesCount; s++ {
			for t := start; t <= end; t++ {
				column.Values[s*length+t-start] = node.at(s, t)
			}
		}
		result.Columns = append(result.Columns, column)
	}
	return result
}
