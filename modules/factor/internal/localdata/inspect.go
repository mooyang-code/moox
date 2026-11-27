package localdata

import (
	"context"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
)

// LongGap 是一个长时间没有数据的标的（设计 3.4，决定 19）。
type LongGap struct {
	Subject string
	// Rows 是缺口的根数，Duration 是对应的时长。
	Rows     int
	Duration time.Duration
	// Stale 为真表示标的到现在还没有新数据（Rows 是最新一根距今的根数）；否则是窗口里已经填充的缺口，填充值仍参与计算。
	Stale bool
}

// LongGapThreshold 是长缺口提示的阈值（设计 3.4，决定 19）：连续填充超过 24 小时；步长不小于 8 小时（如日线）时按 3 期，
// 否则每缺一天都会触发。
func LongGapThreshold(step time.Duration) int {
	if step >= 8*time.Hour {
		return 3
	}
	return int(24*time.Hour/step) + 1
}

// LongGaps 返回数据源里长时间没有数据的标的（按缺口从长到短）：窗口里连续填充超过阈值的缺口，以及到现在还没有新数据超过阈值的标的。
// 只提示，不阻断计算。缺失行只在之后有真实数据时才写，所以仍在持续的缺口表现为最新一根太旧，而不是结尾的填充行。
func (d *Data) LongGaps(src seriesstore.Source) []LongGap {
	st := d.lookup(src)
	if st == nil {
		return nil
	}
	window := st.win.Load()
	if window == nil {
		return nil
	}
	step, err := seriesstore.Step(src.Freq)
	if err != nil {
		return nil
	}
	threshold := LongGapThreshold(step)
	var out []LongGap
	seen := map[string]struct{}{}
	for subject, rows := range window.FilledRuns(threshold) {
		out = append(out, LongGap{Subject: subject, Rows: rows, Duration: time.Duration(rows) * step})
		seen[subject] = struct{}{}
	}
	now, err := d.axisNow(src)
	if err != nil {
		return out
	}
	for _, subject := range window.Subjects() {
		if _, dup := seen[subject]; dup {
			continue
		}
		if latest, ok := window.Latest(subject); ok {
			if behind := int(now.Sub(latest) / step); behind >= threshold {
				out = append(out, LongGap{Subject: subject, Rows: behind, Duration: time.Duration(behind) * step, Stale: true})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rows != out[j].Rows {
			return out[i].Rows > out[j].Rows
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

// WindowStatuses 实现 pipeline.Data：各任务内存窗口的载入状态。
func (d *Data) WindowStatuses() []pipeline.WarmupStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []pipeline.WarmupStatus
	for _, st := range d.sources {
		ids, regs := st.registered()
		var loaded map[string]struct{}
		if window := st.win.Load(); window != nil {
			loaded = make(map[string]struct{}, len(window.Subjects()))
			for _, subject := range window.Subjects() {
				loaded[subject] = struct{}{}
			}
		}
		for _, id := range ids {
			reg := regs[id]
			status := pipeline.WarmupStatus{TaskID: id, State: pipeline.WarmupWarming, ExpectedSubjects: len(reg.subjects)}
			for _, subject := range reg.subjects {
				if _, ok := loaded[subject]; ok {
					status.WarmSubjects++
				}
			}
			if st.ready.Load() {
				status.State = pipeline.WarmupReady
			}
			out = append(out, status)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}

// SubjectsWithoutData 返回任务登记的标的里本地没有任何数据的（排序）与任务登记的标的数，供任务检查现算：
// 部分标的没有数据时预载照常完成，只有这里能看出哪些标的每期都算不出来。任务没登记或读本地失败时返回空。
func (d *Data) SubjectsWithoutData(taskID string, src seriesstore.Source) ([]string, int) {
	st := d.lookup(src)
	if st == nil {
		return nil, 0
	}
	st.regsMu.Lock()
	reg, ok := st.regs[taskID]
	subjects := append([]string(nil), reg.subjects...)
	st.regsMu.Unlock()
	if !ok {
		return nil, 0
	}
	local, err := d.cfg.Store.Subjects(src)
	if err != nil {
		return nil, len(subjects)
	}
	return noDataSubjects(subjects, local, nil), len(subjects)
}

// Short 是回看根数不足的标的：本地连续数据的根数不到任务要求的回看，而 Storage 在本地最早一根之前还有数据（不是新上市的标的）。
type Short struct {
	Subject string
	// Have 是本地连续的根数，Want 是要求的回看根数。
	Have, Want int
	// Earliest 是本地最早的一根。
	Earliest time.Time
}

// Coverage 检查数据源每个标的的本地数据是否满足回看要求（设计 6.2）：根数 ≥ 最大回看根数，或者本地最早一根就是 Storage 的首根
// （在 Storage 保留期内上市、Storage 有多少本地就有多少的新标的）。不满足的列出来，提示导入。
// 没有本地数据的标的不在此列（同步失败由同步状态体现）。
func (d *Data) Coverage(ctx context.Context, src seriesstore.Source) ([]Short, error) {
	st := d.lookup(src)
	if st == nil {
		return nil, nil
	}
	st.mu.Lock()
	subjects, lookback, spec := append([]string(nil), st.subjects...), st.spec.Lookback, st.spec
	exempt := st.exemptNew
	st.mu.Unlock()
	step, err := seriesstore.Step(src.Freq)
	if err != nil {
		return nil, err
	}
	var short []Short
	for _, subject := range subjects {
		b, ok, err := d.cfg.Store.Bounds(src, subject)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		have := int(b.Latest.Sub(b.Earliest)/step) + 1
		if have >= lookback {
			continue
		}
		key := coverageKey{subject: subject, earliest: b.Earliest.Unix(), lookback: lookback}
		st.mu.Lock()
		_, known := st.covered[key]
		st.mu.Unlock()
		if known {
			continue
		}
		first, has, err := d.cfg.Storage.EarliestPeriod(ctx, src.SpaceID, src.DatasetID, src.Freq, []string{subject}, spec.Names())
		if err != nil {
			return nil, err
		}
		if exempt && (!has || !first.Before(b.Earliest)) {
			st.mu.Lock()
			if st.covered == nil {
				st.covered = map[coverageKey]struct{}{}
			}
			st.covered[key] = struct{}{}
			st.mu.Unlock()
			continue
		}
		short = append(short, Short{Subject: subject, Have: have, Want: lookback, Earliest: b.Earliest})
	}
	return short, nil
}
