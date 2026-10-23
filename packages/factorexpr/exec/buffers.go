package exec

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// pool 分配节点缓冲区，并精确统计同时可达的缓冲区字节数。归还时只丢掉引用、交给 GC，不保留空闲列表：
// 保留已归还的数组会让实际占用超过统计（不同需求区间的长度各不相同，很难复用），内存估算就失真了。
// 节点按顺序计算，分配与归还只发生在节点之间，不需要加锁。
type pool struct {
	live int64
	peak int64
}

func newPool() *pool { return &pool{} }

func (p *pool) get(size int) []float64 {
	p.live += int64(size) * 8
	p.peak = max(p.peak, p.live)
	return make([]float64, size)
}

func (p *pool) put(buffer []float64) {
	p.live -= int64(len(buffer)) * 8
}

// Estimate 是一次执行的内存估算（字节）。PeakBuffers 按同样的顺序分配、回收，是执行时峰值的上界：
// 参数取值相同的成员在运行时去重后少算一些节点，实际峰值只会更低；没有可去重的节点时两者完全一致。
type Estimate struct {
	Panel       int64
	PeakBuffers int64
	Result      int64
}

// Total 返回估算总量。
func (e Estimate) Total() int64 { return e.Panel + e.PeakBuffers + e.Result }

// EstimateMemory 估算在 series 条序列、长度为 length 的网格上计算目标区间 [start, end] 的内存（设计 7.5 的计算阶段）。
func EstimateMemory(program *factorexpr.Program, series, length, start, end int) (Estimate, error) {
	need, err := program.Need(start, end)
	if err != nil {
		return Estimate{}, err
	}
	estimate := Estimate{
		Panel:  int64(len(program.Columns))*int64(series)*int64(length)*8 + int64(series)*int64(length),
		Result: int64(len(program.Outputs))*int64(series)*int64(end-start+1)*8 + int64(series)*int64(end-start+1),
	}
	live := int64(0)
	sizes := make([]int64, len(program.Nodes))
	for index, node := range program.Nodes {
		if computed(node) && !need[index].Empty() {
			sizes[index] = int64(series) * int64(need[index].Len()) * 8
			live += sizes[index]
			estimate.PeakBuffers = max(estimate.PeakBuffers, live)
		}
		for _, arg := range node.Args {
			if program.Nodes[arg].LastUse == index && sizes[arg] > 0 {
				live -= sizes[arg]
				sizes[arg] = 0
			}
		}
	}
	return estimate, nil
}

// computed 报告节点是否需要自己的缓冲区：输入列直接引用面板，常量与只由参数决定的节点按标量广播。
func computed(node *factorexpr.ProgramNode) bool {
	return node.Op != factorexpr.OpColumn && node.Op != factorexpr.OpConst && !node.Scalar
}

func checkNeed(program *factorexpr.Program, need []factorexpr.Interval, length int) error {
	for index, interval := range need {
		if interval.Empty() || program.Nodes[index].Op == factorexpr.OpConst || program.Nodes[index].Scalar {
			continue
		}
		if interval.Start < 0 || interval.End >= length {
			return fmt.Errorf("网格不足：计算需要网格下标 [%d, %d]，面板只有 [0, %d]", interval.Start, interval.End, length-1)
		}
	}
	return nil
}
