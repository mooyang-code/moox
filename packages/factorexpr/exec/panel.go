// Package exec 在列式面板上执行因子集的计算图（设计第 7 节）：按拓扑顺序逐节点计算，时序与逐元素节点按序列分块、
// 截面节点按时刻分块并行；每个节点的缓冲区按它自己的需求区间分配，最后一个使用者算完就回收；结果按输出列返回。
package exec

import (
	"fmt"
	"math"
	"sort"
)

// SeriesKey 是一条序列：标的与 series_tag。
type SeriesKey struct {
	Subject string
	Tag     string
}

// Panel 是计算的输入：时间网格（下标 0..Length−1）× 序列 × 输入列。网格上没有源行的位置各列都是 NaN。
// 每列按序列优先存放：序列 s 在网格下标 t 的值位于 data[s*Length+t]。
type Panel struct {
	length   int
	series   []SeriesKey
	index    map[SeriesKey]int
	groups   [][]int // 按 series_tag 分组的序列下标，组内按标的排序；截面算子在组内计算
	columns  []string
	colIndex map[string]int
	data     [][]float64
	present  []bool
}

// NewPanel 创建面板。序列按 (标的, series_tag) 排序，结果与传入顺序无关；全部值初始化为 NaN。
func NewPanel(length int, columns []string, series []SeriesKey) (*Panel, error) {
	if length < 1 {
		return nil, fmt.Errorf("网格长度必须为正数，得到 %d", length)
	}
	panel := &Panel{length: length, index: make(map[SeriesKey]int, len(series)), colIndex: make(map[string]int, len(columns))}
	panel.series = append([]SeriesKey(nil), series...)
	sort.Slice(panel.series, func(i, j int) bool {
		if panel.series[i].Subject != panel.series[j].Subject {
			return panel.series[i].Subject < panel.series[j].Subject
		}
		return panel.series[i].Tag < panel.series[j].Tag
	})
	tagGroups := map[string][]int{}
	var tags []string
	for i, key := range panel.series {
		if _, duplicate := panel.index[key]; duplicate {
			return nil, fmt.Errorf("序列 %s/%s 重复", key.Subject, key.Tag)
		}
		panel.index[key] = i
		if _, ok := tagGroups[key.Tag]; !ok {
			tags = append(tags, key.Tag)
		}
		tagGroups[key.Tag] = append(tagGroups[key.Tag], i)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		panel.groups = append(panel.groups, tagGroups[tag])
	}
	for _, column := range columns {
		if column == "" {
			return nil, fmt.Errorf("列名不能为空")
		}
		if _, duplicate := panel.colIndex[column]; duplicate {
			return nil, fmt.Errorf("列 %s 重复", column)
		}
		panel.colIndex[column] = len(panel.columns)
		panel.columns = append(panel.columns, column)
		values := make([]float64, length*len(panel.series))
		for i := range values {
			values[i] = math.NaN()
		}
		panel.data = append(panel.data, values)
	}
	panel.present = make([]bool, length*len(panel.series))
	return panel, nil
}

// Length 返回网格长度。
func (p *Panel) Length() int { return p.length }

// Series 返回排好序的序列。
func (p *Panel) Series() []SeriesKey { return p.series }

// SeriesIndex 返回序列的下标。
func (p *Panel) SeriesIndex(key SeriesKey) (int, bool) {
	index, ok := p.index[key]
	return index, ok
}

// ColumnIndex 返回列的下标。
func (p *Panel) ColumnIndex(name string) (int, bool) {
	index, ok := p.colIndex[name]
	return index, ok
}

// Set 设置一个值；源数据里的 null 直接传 NaN。
func (p *Panel) Set(column, series, t int, value float64) {
	p.data[column][series*p.length+t] = value
}

// MarkPresent 标记网格下标 t 上该序列有源行；结果只在有源行的目标时刻输出。
func (p *Panel) MarkPresent(series, t int) {
	p.present[series*p.length+t] = true
}

// Value 返回一个输入值（没有源行或源数据为 null 时是 NaN）。
func (p *Panel) Value(column, series, t int) float64 {
	return p.data[column][series*p.length+t]
}

// Present 报告网格下标 t 上该序列是否有源行。
func (p *Panel) Present(series, t int) bool {
	return p.present[series*p.length+t]
}
