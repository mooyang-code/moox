// Package schedule parses and validates archive task execution plans.
//
// 执行计划是带秒的 6 段 cron（robfig/cron v1 语法）加 IANA 时区；保存时展开未来 14 天的计划时刻，
// 取相邻时刻的最小间隔作为任务的执行间隔，小于下限直接拒绝。
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/robfig/cron"
)

const (
	// DefaultMinInterval 是执行间隔的下限。
	DefaultMinInterval = 5 * time.Minute
	// ExpandHorizon 是计算最小间隔时展开计划时刻的时长。
	ExpandHorizon = 14 * 24 * time.Hour
	// maxExpand 限制展开的计划时刻个数；超过即说明间隔远小于下限。
	maxExpand = 40000
)

// Plan 是一个已校验的执行计划。
type Plan struct {
	Cron string
	TZ   string
	loc  *time.Location
	spec cron.Schedule
}

// Parse 解析带秒的 6 段 cron 与时区；时区为空时按 Asia/Shanghai。
func Parse(spec, tz string) (*Plan, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("执行计划不能为空")
	}
	if strings.HasPrefix(spec, "TZ=") || strings.HasPrefix(spec, "CRON_TZ=") || strings.HasPrefix(spec, "@") {
		return nil, errors.New("执行计划必须是带秒的 6 段 cron，时区用单独的字段设置")
	}
	if len(strings.Fields(spec)) != 6 {
		return nil, errors.New("执行计划必须是带秒的 6 段 cron（秒 分 时 日 月 周）")
	}
	tz = strings.TrimSpace(tz)
	if tz == "" {
		tz = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("时区 %q 无效: %w", tz, err)
	}
	parsed, err := cron.Parse(spec)
	if err != nil {
		return nil, fmt.Errorf("执行计划 %q 无法解析: %w", spec, err)
	}
	plan := &Plan{Cron: spec, TZ: tz, loc: loc, spec: parsed}
	if plan.Next(time.Now()).IsZero() {
		return nil, errors.New("执行计划在未来 5 年内没有任何计划时刻")
	}
	return plan, nil
}

// Next 返回严格晚于 after 的下一个计划时刻（UTC）；没有时返回零值。
func (p *Plan) Next(after time.Time) time.Time {
	next := p.spec.Next(after.In(p.loc))
	if next.IsZero() {
		return next
	}
	return next.UTC()
}

// NextN 返回 after 之后的 n 个计划时刻。
func (p *Plan) NextN(after time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for t := p.Next(after); !t.IsZero() && len(out) < n; t = p.Next(t) {
		out = append(out, t)
	}
	return out
}

// MinInterval 展开 from 之后 ExpandHorizon 内的计划时刻，返回相邻时刻的最小间隔。
// 只有一个计划时刻时返回 ExpandHorizon；计划时刻过密（超过 maxExpand 个）时返回 0。
func (p *Plan) MinInterval(from time.Time) time.Duration {
	end := from.Add(ExpandHorizon)
	prev := p.Next(from)
	if prev.IsZero() || prev.After(end) {
		return ExpandHorizon
	}
	min := ExpandHorizon
	for count := 0; count < maxExpand; count++ {
		next := p.Next(prev)
		if next.IsZero() || next.After(end) {
			return min
		}
		if gap := next.Sub(prev); gap < min {
			min = gap
		}
		prev = next
	}
	return 0
}

// CheckInterval 校验最小间隔不低于下限，返回向下取整到秒的最小间隔。
func (p *Plan) CheckInterval(from time.Time, floor time.Duration) (time.Duration, error) {
	gap := p.MinInterval(from)
	if gap < floor {
		return gap, fmt.Errorf("执行计划相邻两次的最小间隔 %s 小于下限 %s", gap.Truncate(time.Second), floor)
	}
	return gap.Truncate(time.Second), nil
}

// Location 返回计划的时区。
func (p *Plan) Location() *time.Location { return p.loc }

// BarSpan 返回一根 K 线的最大跨度 S：1mo 取 31 天，1w 取 7 天，其余取频率时长。
func BarSpan(freq string) (time.Duration, error) {
	f, err := frequency.Parse(freq)
	if err != nil {
		return 0, err
	}
	switch f {
	case frequency.Month1:
		return 31 * 24 * time.Hour, nil
	case frequency.Week1:
		return 7 * 24 * time.Hour, nil
	default:
		return f.Duration(), nil
	}
}

// PartitionUnit 返回频率对应的分区粒度：低于 1d 按空间本地日（day），1d 及以上按月（month）。
func PartitionUnit(freq string) (string, error) {
	f, err := frequency.Parse(freq)
	if err != nil {
		return "", err
	}
	switch f {
	case frequency.Day1, frequency.Week1, frequency.Month1:
		return "month", nil
	}
	return "day", nil
}

// ReadSpan 返回一次读取区间的长度：执行间隔 + 最少回看 + 一根 K 线的最大跨度。
func ReadSpan(interval, lookback time.Duration, freq string) (time.Duration, error) {
	span, err := BarSpan(freq)
	if err != nil {
		return 0, err
	}
	return interval + lookback + span, nil
}

// DefaultPlan 返回空间与频率的默认执行计划（北京时间）。
func DefaultPlan(spaceID, freq string) (spec, tz string) {
	tz = "Asia/Shanghai"
	f, err := frequency.Parse(freq)
	hourly := err == nil && (f.Duration() >= time.Hour || f == frequency.Month1)
	switch strings.ToLower(strings.TrimSpace(spaceID)) {
	case "stockcn":
		return "43 4 17 * * *", tz
	case "stockhk":
		return "43 34 17 * * *", tz
	case "stockus":
		return "43 4 6 * * *", tz
	default:
		if hourly {
			return "43 34 8 * * *", tz
		}
		return "43 34 * * * *", tz
	}
}

// AvoidanceWarnings 检查计划时刻是否落在避让区间：每分钟只在第 25–55 秒发起请求，
// 每小时 :58 到下一小时 :04 之间不发起请求。落入的给出「实际读取从何时开始」的提示。times 为 UTC。
func AvoidanceWarnings(times []time.Time) []string {
	var warnings []string
	seen := map[string]bool{}
	for _, t := range times {
		t = t.UTC()
		hour := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
		var actual time.Time
		switch minute := t.Minute(); {
		case minute >= 58:
			actual = hour.Add(time.Hour + 4*time.Minute + 25*time.Second)
		case minute < 4 || (minute == 4 && t.Second() < 25):
			actual = hour.Add(4*time.Minute + 25*time.Second)
		case t.Second() < 25:
			actual = hour.Add(time.Duration(minute)*time.Minute + 25*time.Second)
		case t.Second() > 55:
			actual = hour.Add(time.Duration(minute+1)*time.Minute + 25*time.Second)
		default:
			continue
		}
		message := fmt.Sprintf("计划时刻 %s 落在避让区间，实际读取从 %s 开始", t.Format("15:04:05"), actual.Format("15:04:05"))
		if !seen[message] {
			seen[message] = true
			warnings = append(warnings, message)
		}
	}
	return warnings
}

// PartitionOf 返回数据时间所属的分区：日分区按空间时区的本地日切分，月分区按「data_time + 12 小时」的 UTC 日期归月。
// 返回分区键（日 2006-01-02、月 2006-01）与 data_time 上的 [start, end) 范围（UTC）。
func PartitionOf(unit string, loc *time.Location, dataTime time.Time) (key string, start, end time.Time) {
	if unit == "month" {
		// 归月用的是 data_time + 12 小时的 UTC 日期；分区在 data_time 上的范围要把这 12 小时减回去：
		// [当月 1 日 UTC 零点 − 12 小时, 下月 1 日 UTC 零点 − 12 小时)。
		shifted := dataTime.UTC().Add(12 * time.Hour)
		first := time.Date(shifted.Year(), shifted.Month(), 1, 0, 0, 0, 0, time.UTC)
		return first.Format("2006-01"), first.Add(-12 * time.Hour), first.AddDate(0, 1, 0).Add(-12 * time.Hour)
	}
	local := dataTime.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return day.Format("2006-01-02"), day.UTC(), day.AddDate(0, 0, 1).UTC()
}

// PartitionClose 返回分区的收盘时间：日分区有本地收盘时刻（如 "15:00"）的空间取该日的本地收盘时刻，
// 否则取分区结束；月分区取「分区结束 + 一根 K 线的最大跨度」。
func PartitionClose(unit string, loc *time.Location, localClose, freq string, start, end time.Time) (time.Time, error) {
	if unit == "month" {
		span, err := BarSpan(freq)
		if err != nil {
			return time.Time{}, err
		}
		return end.Add(span), nil
	}
	if strings.TrimSpace(localClose) == "" {
		return end, nil
	}
	clock, err := time.Parse("15:04", localClose)
	if err != nil {
		return time.Time{}, fmt.Errorf("本地收盘时刻 %q 无效: %w", localClose, err)
	}
	local := start.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), clock.Hour(), clock.Minute(), 0, 0, loc).UTC(), nil
}

// ExportEstimate 返回分区预计的导出时间：第一个不早于「收盘时间 + 最少回看」的计划时刻。
func (p *Plan) ExportEstimate(close time.Time, lookback time.Duration) time.Time {
	return p.Next(close.Add(lookback).Add(-time.Second))
}
