package runner

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/seriesformat"
)

// sessionAxis 让连续性检查按市场的交易时段判断相邻：有交易时段的市场（A 股）里午休、隔夜、休市日没有 K 线，
// 不能按固定步长统计缺期。行时间先换成交易序号 × 周期（相邻两根交易 K 线恰好差一个周期），再交给通用的检查器；
// 报告里的时间再换回真实时间。7×24 市场与未登记的空间返回 nil，按真实时间检查。
type sessionAxis struct {
	clock barclock.Clock
	step  time.Duration
}

func newSessionAxis(space string, step time.Duration) (*sessionAxis, error) {
	market, err := barclock.MarketFor(space)
	if err != nil || market.Alignment != barclock.AlignmentSession {
		return nil, nil
	}
	clock, err := market.Clock(step)
	if err != nil {
		return nil, fmt.Errorf("空间 %s 的交易时段不支持周期 %s：%w", space, step, err)
	}
	return &sessionAxis{clock: clock, step: step}, nil
}

// toAxis 把交易桶的 data_time 换成轴上时间；不是交易桶起点（午休、休市、未对齐）时报错。
func (a *sessionAxis) toAxis(t time.Time) (time.Time, error) {
	items, err := a.clock.Between(t, t.Add(a.step), 1)
	if err != nil {
		return time.Time{}, err
	}
	if len(items) == 0 || !items[0].Key.Equal(t) {
		return time.Time{}, fmt.Errorf("时间 %s 不是交易时段内按 %s 对齐的 K 线", t.Format(time.RFC3339), a.step)
	}
	n, err := a.clock.Ordinal(items[0])
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(n*int64(a.step/time.Second), 0).UTC(), nil
}

func (a *sessionAxis) fromAxis(x time.Time) time.Time {
	b, err := a.clock.BucketAtOrdinal(x.Unix() / int64(a.step/time.Second))
	if err != nil {
		return x
	}
	return b.Key
}

// batch 返回行时间换到轴上的批（只供检查器使用，不改原批）。
func (a *sessionAxis) batch(b *seriesformat.Batch) (*seriesformat.Batch, error) {
	out := &seriesformat.Batch{Times: make([]time.Time, len(b.Times)), Tags: b.Tags, Cols: b.Cols}
	for i, t := range b.Times {
		x, err := a.toAxis(t)
		if err != nil {
			return nil, err
		}
		out.Times[i] = x
	}
	return out, nil
}

// report 把报告里的时间换回真实时间。
func (a *sessionAxis) report(r seriesformat.Report) seriesformat.Report {
	if r.Rows > 0 {
		r.First, r.Last = a.fromAxis(r.First), a.fromAxis(r.Last)
	}
	missing := make([]time.Time, len(r.Missing))
	for i, x := range r.Missing {
		missing[i] = a.fromAxis(x)
	}
	r.Missing = missing
	return r
}
