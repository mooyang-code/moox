package pipeline

import (
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
)

// 每格开销的估算常量（字节，带余量），见 docs/计划/因子表达式引擎详细设计.md 7.5：
//   - frameCellBytes：storageio.Frame 行式数据的一个单元（interface 值、装箱的 float64、切片分摊），现在只用于目标区间的行；
//   - resultCellBytes：计算结果转换成逐行结果后的一个单元（ItemResult.Rows 里的值与行切片分摊）；
//   - fieldBytes：组装与写入阶段一行里的一个字段（map 项、键、装箱值）。
const (
	frameCellBytes  = 48
	resultCellBytes = 40
	fieldBytes      = 96
)

// ErrMemoryBudget 表示一个计算分块（实时周期视为 1 个周期的分块）估算的峰值内存超过 compute.max_memory。
var ErrMemoryBudget = errors.New("因子计算的估算内存超过上限")

// MemoryEstimate 是一个分块完整生命周期的峰值内存估算：目标区间的行、面板与节点缓冲区、逐行结果、组装与写入。
type MemoryEstimate struct {
	Read     int64
	Compute  int64
	Result   int64
	Assemble int64
}

func (m MemoryEstimate) Total() int64 { return m.Read + m.Compute + m.Result + m.Assemble }

// EstimateChunkMemory 在读取之前按成员、序列数（标的数 × 标签数的估计）、目标周期数与携带列数估算峰值内存。
// 编译失败的定义不参与（它们本期按失败处理，不占计算内存）。参数槽按声明范围取最坏回看，所以估算是上界。
func EstimateChunkMemory(members []domain.MemberDef, series, periods, carryColumns int) (MemoryEstimate, error) {
	if series <= 0 || periods <= 0 {
		return MemoryEstimate{}, nil
	}
	inputs := make([]factorexpr.ProgramInput, 0, len(members))
	outputs := 0
	for _, member := range members {
		def, err := domain.CompileDefinition(member.Factor)
		if err != nil {
			continue
		}
		inputs = append(inputs, factorexpr.ProgramInput{Key: member.Key, FactorID: def.FactorID, Factor: def.Compiled()})
		outputs += len(def.Outputs) + len(def.Compiled().Params)
	}
	if len(inputs) == 0 {
		return MemoryEstimate{}, nil
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		return MemoryEstimate{}, err
	}
	lookback := max(1, program.Lookback)
	length := lookback - 1 + periods
	computeEstimate, err := exec.EstimateMemory(program, series, length, lookback-1, length-1)
	if err != nil {
		return MemoryEstimate{}, err
	}
	cells := func(rows, columns int) int64 { return int64(series) * int64(rows) * int64(columns) }
	return MemoryEstimate{
		// 回看窗口是内存里的常驻列式窗口，不再按帧计入；这里只有目标区间的行（组装写出携带列用）。
		Read:     cells(periods, 2+carryColumns) * frameCellBytes,
		Compute:  computeEstimate.Total(),
		Result:   cells(periods, 2+outputs) * resultCellBytes,
		Assemble: cells(periods, outputs+carryColumns) * fieldBytes,
	}, nil
}

// MaxPeriodsWithin 返回不超过 limit、估算峰值内存不超过 budget 的最大目标周期数；1 个周期也超限时返回 ErrMemoryBudget。
// 估算随周期数单调增长，二分查找。budget ≤ 0 表示不限制。
func MaxPeriodsWithin(factors []domain.MemberDef, series, carryColumns, limit int, budget int64) (int, error) {
	if budget <= 0 || limit <= 1 {
		if budget > 0 {
			if err := withinBudget(factors, series, 1, carryColumns, budget); err != nil {
				return 0, err
			}
		}
		return max(1, limit), nil
	}
	if err := withinBudget(factors, series, 1, carryColumns, budget); err != nil {
		return 0, err
	}
	lo, hi := 1, limit
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		estimate, err := EstimateChunkMemory(factors, series, mid, carryColumns)
		if err != nil {
			return 0, err
		}
		if estimate.Total() <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, nil
}

// CheckChunkMemory 检查一次计算（series 条序列 × periods 个目标周期）的估算峰值内存是否不超过 budget，超限返回 ErrMemoryBudget。
func CheckChunkMemory(factors []domain.MemberDef, series, periods, carryColumns int, budget int64) error {
	return withinBudget(factors, series, periods, carryColumns, budget)
}

func withinBudget(factors []domain.MemberDef, series, periods, carryColumns int, budget int64) error {
	estimate, err := EstimateChunkMemory(factors, series, periods, carryColumns)
	if err != nil {
		return err
	}
	if budget > 0 && estimate.Total() > budget {
		return fmt.Errorf("%w：%d 个序列 × %d 期估算 %d 字节（读取 %d、计算 %d、结果 %d、组装 %d），上限 %d；任务里的因子需要拆分",
			ErrMemoryBudget, series, periods, estimate.Total(), estimate.Read, estimate.Compute, estimate.Result, estimate.Assemble, budget)
	}
	return nil
}
