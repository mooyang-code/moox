package seriesformat

import (
	"fmt"
	"sort"
	"time"
)

// Checker 是与格式无关的校验层：按频率对齐、按序列严格递增（不重复、不倒退）、统计缺的周期。
// 数据文件逐批喂给 Check，最后 Finish 得到连续性报告。
type Checker struct {
	step time.Duration
	last map[string]time.Time // series_tag -> 已见的最后一个时间
	// MaxMissing 是报告里最多列出的缺失周期数（统计总数不受限）。
	MaxMissing int
	missing    []time.Time
	missingN   int
	lastAny    time.Time // 所有序列里已见的最大时间
	rows       int
	first      time.Time
	lastAll    time.Time
}

// NewChecker 创建校验器；step 是频率对应的周期时长。
func NewChecker(step time.Duration) *Checker {
	return &Checker{step: step, last: map[string]time.Time{}, MaxMissing: 100}
}

// Check 校验一批行。时间必须按频率对齐，同一个 series_tag 内必须严格递增，整个文件按时间升序
// （多个序列时同一时间点的行相邻；导入按时间边界分批，一批的时间区间会整段替换，序列分块排列的文件会互相覆盖）。
func (c *Checker) Check(b *Batch) error {
	for i := 0; i < b.Len(); i++ {
		at, tag := b.Times[i], b.Tags[i]
		if at.Nanosecond() != 0 || at.Unix()%int64(c.step/time.Second) != 0 {
			return fmt.Errorf("时间 %s 没有按 %s 周期对齐", at.Format(time.RFC3339), c.step)
		}
		if at.Before(c.lastAny) {
			return fmt.Errorf("时间 %s 早于文件里已出现的 %s：多个序列的文件要整体按时间升序排列（同一时间点的各序列相邻），不能按序列分块排列", at.Format(time.RFC3339), c.lastAny.Format(time.RFC3339))
		}
		c.lastAny = at
		if prev, ok := c.last[tag]; ok {
			switch {
			case at.Equal(prev):
				return fmt.Errorf("序列 %q 在 %s 有重复行", tag, at.Format(time.RFC3339))
			case at.Before(prev):
				return fmt.Errorf("序列 %q 的时间 %s 早于上一行 %s：文件需要先按时间升序排序", tag, at.Format(time.RFC3339), prev.Format(time.RFC3339))
			}
			for gap := prev.Add(c.step); gap.Before(at); gap = gap.Add(c.step) {
				c.missingN++
				if len(c.missing) < c.MaxMissing {
					c.missing = append(c.missing, gap)
				}
			}
		}
		c.last[tag] = at
		if c.rows == 0 || at.Before(c.first) {
			c.first = at
		}
		if at.After(c.lastAll) {
			c.lastAll = at
		}
		c.rows++
	}
	return nil
}

// Report 是文件的连续性报告。
type Report struct {
	Rows int
	// First、Last 是文件里最早、最晚的时间（没有行时为零值）。
	First, Last time.Time
	// MissingCount 是序列内部缺的周期总数，Missing 是其中前若干个（已排序）。
	MissingCount int
	Missing      []time.Time
}

// Finish 返回报告。
func (c *Checker) Finish() Report {
	out := append([]time.Time(nil), c.missing...)
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return Report{Rows: c.rows, First: c.first, Last: c.lastAll, MissingCount: c.missingN, Missing: out}
}
