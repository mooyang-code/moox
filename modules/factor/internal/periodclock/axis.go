package periodclock

import (
	"fmt"
	"time"
)

// Axis 是一个空间、一个频率上真实时间与轴上时间的换算，供引擎的换算边界使用；轴上的周期数学一律用 Continuous。
type Axis struct {
	clock Clock
	freq  string
	step  time.Duration
}

// AxisFor 返回空间 spaceID、频率 freq 的换算。
func AxisFor(spaceID, freq string) (Axis, error) {
	clock, err := ForSpace(spaceID)
	if err != nil {
		return Axis{}, err
	}
	step, err := clock.Duration(freq)
	if err != nil {
		return Axis{}, err
	}
	return Axis{clock: clock, freq: freq, step: step}, nil
}

// Step 是周期时长（轴上相邻两期的间隔）。
func (a Axis) Step() time.Duration { return a.step }

// Identity 报告换算是否恒等（7×24 市场）。
func (a Axis) Identity() bool {
	_, ok := a.clock.(Continuous)
	return ok
}

// ToAxis 把真实的桶起点换成轴上时间。
func (a Axis) ToAxis(t time.Time) (time.Time, error) { return a.clock.ToAxis(t, a.freq) }

// FromAxis 把轴上时间换回真实的桶起点。
func (a Axis) FromAxis(x time.Time) (time.Time, error) { return a.clock.FromAxis(x, a.freq) }

// AxisEnd 返回真实时刻 t 在轴上的排他终点（收盘不晚于 t 的最新一根 + 周期）。
func (a Axis) AxisEnd(t time.Time) (time.Time, error) { return a.clock.AxisEnd(t, a.freq) }

// FromAxisEnd 把轴上的排他终点换回真实的排他终点：最后一根的真实起点 + 周期（日内桶就是它的收盘时刻）。
// 不取“下一根的起点”：那可能跨到下一个交易日，超出交易日历的覆盖范围。
func (a Axis) FromAxisEnd(x time.Time) (time.Time, error) {
	last, err := a.FromAxis(x.Add(-a.step))
	if err != nil {
		return time.Time{}, err
	}
	return last.Add(a.step), nil
}

// EndToAxis 把真实的排他终点换到轴上：t 必须是某个桶的起点或终点（7×24 市场即按周期对齐）。
func (a Axis) EndToAxis(t time.Time) (time.Time, error) {
	t = t.UTC()
	if x, err := a.ToAxis(t); err == nil {
		return x, nil
	}
	x, err := a.AxisEnd(t)
	if err != nil {
		return time.Time{}, err
	}
	if end, err := a.FromAxisEnd(x); err == nil && end.Equal(t) {
		return x, nil
	}
	return time.Time{}, fmt.Errorf("时间 %s 不是 %s 的周期边界", t.Format(time.RFC3339), a.freq)
}

// Format 把轴上时间换回真实时间并格式化（日志与报错用）；换不回时标出轴上的原值。
func (a Axis) Format(x time.Time) string {
	if x.IsZero() {
		return ""
	}
	if a.Identity() {
		return x.UTC().Format(time.RFC3339)
	}
	at, err := a.FromAxis(x)
	if err != nil {
		return x.UTC().Format(time.RFC3339) + "（轴上）"
	}
	return at.Format(time.RFC3339)
}

// FormatPeriod 按空间与频率把轴上时间格式化成真实时间（日志与报错用）。
func FormatPeriod(spaceID, freq string, x time.Time) string {
	axis, err := AxisFor(spaceID, freq)
	if err != nil {
		return x.UTC().Format(time.RFC3339)
	}
	return axis.Format(x)
}
