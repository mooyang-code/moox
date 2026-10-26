package engine

import (
	"fmt"
	"strings"
)

// 内存配置自适应（设计第 8 节）：按总内存给出进程内存预算、窗口上限和单次计算上限的建议值。

const gib = int64(1) << 30

// MemoryPlan 是一份内存配置建议。
type MemoryPlan struct {
	// TotalMemory 是探测到的总内存 M。
	TotalMemory int64
	// TotalBudget 是进程内存预算：M × 60%。
	TotalBudget int64
	// BlockCache 是 Pebble 块缓存 C。
	BlockCache int64
	// Window 是所有内存窗口常驻内存的上限 W。
	Window int64
	// Compute 是单次计算分块的内存上限。
	Compute int64
	// Parallel 是计算并行数：计划启用的任务数与 CPU 核数中的较小值。
	Parallel int
	Warnings []string
}

// PlanMemory 按总内存、CPU 核数、计划启用的任务数和块缓存给出建议：
//   - memory.total_budget = M × 60%；
//   - window.max_memory = (总预算 − C) × 40%（窗口常驻，计算要留出余量）；
//   - compute.max_memory = max(1GiB, (总预算 − W − C) × 50% ÷ 并行数)。
//
// W + C 超过总预算的 2/3，或总预算 − W − C 小于 1GiB 时给出警告。
func PlanMemory(total int64, cpus, sets int, blockCache int64) MemoryPlan {
	if cpus < 1 {
		cpus = 1
	}
	if sets < 1 {
		sets = 1
	}
	plan := MemoryPlan{TotalMemory: total, BlockCache: blockCache, Parallel: min(cpus, sets)}
	plan.TotalBudget = total * 6 / 10
	if free := plan.TotalBudget - blockCache; free > 0 {
		plan.Window = free * 4 / 10
	}
	headroom := plan.TotalBudget - plan.Window - plan.BlockCache
	plan.Compute = gib
	if headroom > 0 {
		plan.Compute = max(gib, headroom/2/int64(plan.Parallel))
	}
	if resident := plan.Window + plan.BlockCache; resident*3 > plan.TotalBudget*2 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("窗口与块缓存常驻 %s，超过总预算 %s 的 2/3", formatBytes(resident), formatBytes(plan.TotalBudget)))
	}
	if headroom < gib {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("总预算扣除窗口与块缓存后只剩 %s，不足 1GiB，计算会经常排队", formatBytes(max(headroom, 0))))
	}
	return plan
}

// WindowEstimate 是按回看数量 N 估算的窗口内存（设计 8.1，决定 21）：序列数 × (N + 余量) × (数值字段数 × 8 + 1) 字节，
// 与 serieswindow.BytesPerSeries 一致。headroom 为 0 时取窗口长度的 1/4（至少 16 根）。
func WindowEstimate(series, lookback, numericFields, headroom int) int64 {
	if headroom <= 0 {
		headroom = max(lookback/4, 16)
	}
	return int64(series) * int64(lookback+headroom) * int64(numericFields*8+1)
}

// CheckWindowEstimate 把窗口内存估算与建议的窗口上限比较，超过时给出警告（空串表示放得下）。
func (p MemoryPlan) CheckWindowEstimate(estimate int64) string {
	if estimate <= p.Window {
		return ""
	}
	return fmt.Sprintf("窗口内存估算 %s，超过建议的窗口上限 %s：调小 window.lookback、减少标的数，或加内存", formatBytes(estimate), formatBytes(p.Window))
}

// Summary 是供人看的多行说明。
func (p MemoryPlan) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "总内存 M            %s\n", formatBytes(p.TotalMemory))
	fmt.Fprintf(&b, "总预算（M × 60%%）    %s\n", formatBytes(p.TotalBudget))
	fmt.Fprintf(&b, "块缓存 C            %s\n", formatBytes(p.BlockCache))
	fmt.Fprintf(&b, "窗口上限 W          %s\n", formatBytes(p.Window))
	fmt.Fprintf(&b, "单次计算上限        %s（并行 %d）\n", formatBytes(p.Compute), p.Parallel)
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "警告：%s\n", w)
	}
	return b.String()
}

// formatBytes 把字节数写成配置里可以直接用的形式（整 GiB / MiB 用后缀）。
func formatBytes(n int64) string {
	switch {
	case n >= gib && n%gib == 0:
		return fmt.Sprintf("%dGiB", n/gib)
	case n >= gib:
		return fmt.Sprintf("%.1fGiB", float64(n)/float64(gib))
	default:
		return fmt.Sprintf("%dMiB", n>>20)
	}
}

// ConfigValue 是写进 engine.yaml 的字节数写法：向下取整到 MiB，整 GiB 用 GiB。
func ConfigValue(n int64) string {
	n = n >> 20 << 20
	if n >= gib && n%gib == 0 {
		return fmt.Sprintf("%dGiB", n/gib)
	}
	return fmt.Sprintf("%dMiB", n>>20)
}
