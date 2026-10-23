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
// 常量与只由参数决定的节点按标量（每条序列一个值）广播，不需要行，返回空区间。参数槽按声明范围取最坏扩展
// （Back 最大、Shift 最小），所以任何合法取值需要的行都在区间内。
//
// 需求区间只决定算哪些点，不影响任何一个点的值（点纯性，设计 5.1）。
func (p *Program) Need(targetStart, targetEnd int) ([]Interval, error) {
	return p.NeedWith(targetStart, targetEnd, func(node *ProgramNode) (int, int) { return node.Back, node.Shift })
}

// NeedWith 与 Need 相同，只是每个节点的扩展由 extent 给出：执行器按本次的参数取值算出实际扩展（各序列取最大的 back、
// 最小的 shift），默认窗口 20、声明上限 120 的节点只算需要的 20 根，不按上限多算。
func (p *Program) NeedWith(targetStart, targetEnd int, extent func(*ProgramNode) (back, shift int)) ([]Interval, error) {
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
		if node.Op == OpConst || node.Scalar {
			need[index] = emptyInterval
			continue
		}
		back, shift := extent(node)
		child := Interval{Start: interval.Start - back, End: interval.End - shift}
		for _, arg := range node.Args {
			if p.Nodes[arg].Op == OpConst || p.Nodes[arg].Scalar {
				continue
			}
			need[arg] = need[arg].union(child)
		}
	}
	return need, nil
}
