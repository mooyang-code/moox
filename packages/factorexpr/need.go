package factorexpr

import "fmt"

// Interval 是网格下标上的闭区间；Empty 为真时表示不需要计算任何行。
type Interval struct {
	Start int
	End   int
}

// Empty 报告区间是否为空。
func (i Interval) Empty() bool { return i.Start > i.End }

// Len 返回区间包含的行数。
func (i Interval) Len() int {
	if i.Empty() {
		return 0
	}
	return i.End - i.Start + 1
}

func (i Interval) union(other Interval) Interval {
	switch {
	case i.Empty():
		return other
	case other.Empty():
		return i
	}
	return Interval{Start: min(i.Start, other.Start), End: max(i.End, other.End)}
}

var emptyInterval = Interval{Start: 0, End: -1}

// Need 自顶向下传播每个节点需要计算的行（设计 6.4）：输出节点需要目标行 [targetStart, targetEnd]；时序算子把需求区间
// 向前扩展 Back 行、整体前移 Shift 行后传给子节点；逐元素与截面算子原样传递；被多处引用时取并集（凸包）。
// 常量节点按标量广播，不需要行，返回空区间。
//
// 需求区间只决定算哪些点，不影响任何一个点的值（点纯性，设计 5.1）。
func (p *Program) Need(targetStart, targetEnd int) ([]Interval, error) {
	if targetStart > targetEnd {
		return nil, fmt.Errorf("目标区间 [%d, %d] 为空", targetStart, targetEnd)
	}
	need := make([]Interval, len(p.Nodes))
	for i := range need {
		need[i] = emptyInterval
	}
	target := Interval{Start: targetStart, End: targetEnd}
	for _, output := range p.Outputs {
		need[output.Node] = need[output.Node].union(target)
	}
	for index := len(p.Nodes) - 1; index >= 0; index-- {
		node, interval := p.Nodes[index], need[index]
		if interval.Empty() {
			continue
		}
		if node.Op == OpConst {
			need[index] = emptyInterval
			continue
		}
		back, shift := node.Function.Back(node.Ints), node.Function.Shift(node.Ints)
		child := Interval{Start: interval.Start - back, End: interval.End - shift}
		for _, arg := range node.Args {
			if p.Nodes[arg].Op == OpConst {
				continue
			}
			need[arg] = need[arg].union(child)
		}
	}
	return need, nil
}
