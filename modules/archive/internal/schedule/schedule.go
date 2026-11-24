// Package schedule 计算归档任务的分区范围、收盘时间与读取避让窗口（纯函数）。
//
// mgr 每分钟第 25 秒给每个启用任务生成一条 follow 运行；避让规则写死两个窗口：每分钟只在第 25～55 秒读取的秒窗口
// 与整点窗口 :58～:04（UTC），都由 worker 的 pacing 执行，导出与定稿不读 Storage、不受它们约束。
package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
)

const (
	// AllowFromSecond、AllowUntilSecond：每分钟只在 [第 25 秒, 第 55 秒) 读取 Storage，避开采集函数在第 0 秒的触发与写入。
	AllowFromSecond  = 25
	AllowUntilSecond = 55
	// HourQuietFrom、HourQuietUntil：每小时 [:58:00, :04:00)（UTC，跨整点，小时内的秒偏移）不读取，避开 1h 及以上数据集在整点的集中收盘。
	HourQuietFrom  = 58 * 60
	HourQuietUntil = 4 * 60

	// RescanProgressLabel 是 worker 在整段重读进行中写进 follow、manual 运行消息的标记；mgr 的健康规则据此判断重读是否进行中。
	RescanProgressLabel = "整段重读进度"
)

// InHourQuiet 报告 t 是否落在整点窗口 [:58:00, :04:00) 里（UTC）。
func InHourQuiet(t time.Time) bool {
	t = t.UTC()
	offset := t.Minute()*60 + t.Second()
	return offset >= HourQuietFrom || offset < HourQuietUntil
}

// HourQuietEnd 返回 t 所在整点窗口的结束时刻（t 必须落在窗口里）。
func HourQuietEnd(t time.Time) time.Time {
	t = t.UTC()
	end := t.Truncate(time.Hour).Add(HourQuietUntil * time.Second)
	if !end.After(t) {
		end = end.Add(time.Hour)
	}
	return end
}

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
