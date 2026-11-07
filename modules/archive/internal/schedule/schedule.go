// Package schedule 计算归档任务的拉取边界、复核边界与分区范围（纯函数）。
//
// mgr 每分钟第 25 秒检查一次：每个任务按频率 × N 对齐出「最近一个已收盘的拉取边界」，边界比上次记下的晚才生成运行；
// 低于 1d 的任务另有每小时第 34 分的复核；1d 及以上每天一个边界，只生成复核。每小时 :58～:04 整次跳过。
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
)

const (
	// CheckSecond 是 mgr 每分钟检查的秒数（与 trpc 触发器 "25 * * * * *" 一致）。
	CheckSecond = 25
	// ReviewMinute 是低于 1d 的任务每小时复核的分钟（UTC）。
	ReviewMinute = 34
	// TickExtraBars 是到点拉取在新收盘的几根之外多读的根数，带上刚收盘那根的晚到写入。
	TickExtraBars = 2
	// tickMaxBehindBars：上次边界比本次边界早这么多根以上时，到点拉取只读最近 N + 2 根，更早的交给复核。
	tickMaxBehindBars = 60
	// DefaultPullEveryBars 与 MaxPullEveryBars 是 N 的默认值与上限。
	DefaultPullEveryBars = 1
	MaxPullEveryBars     = 1440
	// avoidFromMinute～avoidUntilMinute 是每小时的避让区间 :58～:04（与 worker 的整点静默一致）。
	avoidFromMinute  = 58
	avoidUntilMinute = 4
	// maxEnumerate 限制枚举边界的次数，防止异常输入死循环。
	maxEnumerate = 100000
)

// Action 是一次检查对一个任务的处理。
type Action int

const (
	// ActionNone：不到点，或落在避让区间，什么都不做、不写记录。
	ActionNone Action = iota
	// ActionReview：生成复核运行（scheduled），沿用回看规则读取并推进水位。
	ActionReview
	// ActionTick：生成到点拉取运行（tick），只读新收盘的几根、不推进水位。
	ActionTick
)

// Timing 是一个任务算拉取边界需要的时序信息。
type Timing struct {
	Freq string
	// PullEveryBars 是 N：每收盘 N 根拉取一次；1d 及以上忽略。0 按默认 1。
	PullEveryBars int
	// Loc 与 LocalClose 是空间时区与本地收盘时刻（"15:00"，空串表示全天交易）；只影响 1d 及以上。
	Loc        *time.Location
	LocalClose string
}

// timing 是解析后的时序信息。
type timing struct {
	daily    bool
	bar      time.Duration // 低于 1d 时一根 K 线的时长
	step     time.Duration // 低于 1d 时的拉取步长 = bar × N
	n        int
	loc      *time.Location
	hasClose bool
	closeH   int
	closeM   int
}

// Daily 报告频率是否为 1d 及以上（1d、1w、1mo）。
func Daily(freq string) (bool, error) {
	f, err := frequency.Parse(freq)
	if err != nil {
		return false, err
	}
	switch f {
	case frequency.Day1, frequency.Week1, frequency.Month1:
		return true, nil
	}
	return false, nil
}

// ReviewInterval 返回复核间隔：低于 1d 为 1 小时，1d 及以上为 1 天。
func ReviewInterval(freq string) (time.Duration, error) {
	daily, err := Daily(freq)
	if err != nil {
		return 0, err
	}
	if daily {
		return 24 * time.Hour, nil
	}
	return time.Hour, nil
}

// ValidatePullEveryBars 校验 N 的范围（1～1440）。
func ValidatePullEveryBars(n int) error {
	if n < 1 || n > MaxPullEveryBars {
		return fmt.Errorf("pull_every_bars 必须在 1～%d 之间，当前 %d", MaxPullEveryBars, n)
	}
	return nil
}

func (t Timing) parse() (timing, error) {
	f, err := frequency.Parse(t.Freq)
	if err != nil {
		return timing{}, err
	}
	n := t.PullEveryBars
	if n == 0 {
		n = DefaultPullEveryBars
	}
	if err := ValidatePullEveryBars(n); err != nil {
		return timing{}, err
	}
	out := timing{n: n, loc: t.Loc}
	if out.loc == nil {
		out.loc = time.UTC
	}
	switch f {
	case frequency.Day1, frequency.Week1, frequency.Month1:
		out.daily = true
		if close := strings.TrimSpace(t.LocalClose); close != "" {
			clock, err := time.Parse("15:04", close)
			if err != nil {
				return timing{}, fmt.Errorf("本地收盘时刻 %q 无效: %w", t.LocalClose, err)
			}
			out.hasClose, out.closeH, out.closeM = true, clock.Hour(), clock.Minute()
		}
		return out, nil
	}
	out.bar = f.Duration()
	if out.bar <= 0 {
		return timing{}, fmt.Errorf("频率 %q 没有固定时长", t.Freq)
	}
	out.step = out.bar * time.Duration(n)
	return out, nil
}

// boundary 返回 ≤ now 的最晚一个拉取边界。
func (t timing) boundary(now time.Time) time.Time {
	now = now.UTC()
	if !t.daily {
		// 按 Unix 纪元向下取整到步长的倍数（UTC，与 K 线开盘对齐）。
		ns, step := now.UnixNano(), int64(t.step)
		floor := ns / step * step
		if floor > ns {
			floor -= step
		}
		return time.Unix(0, floor).UTC()
	}
	if !t.hasClose {
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	}
	local := now.In(t.loc)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), t.closeH, t.closeM, 0, 0, t.loc)
	if candidate.After(now) {
		candidate = time.Date(local.Year(), local.Month(), local.Day()-1, t.closeH, t.closeM, 0, 0, t.loc)
	}
	return candidate.UTC()
}

// next 返回边界 b 之后的下一个拉取边界。
func (t timing) next(b time.Time) time.Time {
	if !t.daily {
		return b.Add(t.step)
	}
	if !t.hasClose {
		return b.AddDate(0, 0, 1)
	}
	// 按当地日期重新构造，跨夏令时也落在本地收盘时刻。
	local := b.In(t.loc)
	return time.Date(local.Year(), local.Month(), local.Day()+1, t.closeH, t.closeM, 0, 0, t.loc).UTC()
}

// reviewBoundary 返回 ≤ now 的最晚一个复核边界：低于 1d 为每小时第 34 分（UTC），1d 及以上与拉取边界相同。
func (t timing) reviewBoundary(now time.Time) time.Time {
	if t.daily {
		return t.boundary(now)
	}
	now = now.UTC()
	r := now.Truncate(time.Hour).Add(ReviewMinute * time.Minute)
	if r.After(now) {
		r = r.Add(-time.Hour)
	}
	return r
}

// TickBoundary 返回 ≤ now 的最晚一个拉取边界：低于 1d 按「频率 × N」从 Unix 纪元对齐（UTC）；
// 1d 及以上每天一个，空间有本地收盘时刻时取最近一个本地收盘，否则取 UTC 0 点。
func TickBoundary(t Timing, now time.Time) (time.Time, error) {
	parsed, err := t.parse()
	if err != nil {
		return time.Time{}, err
	}
	return parsed.boundary(now), nil
}

// ReviewBoundary 返回 ≤ now 的最晚一个复核边界。
func ReviewBoundary(t Timing, now time.Time) (time.Time, error) {
	parsed, err := t.parse()
	if err != nil {
		return time.Time{}, err
	}
	return parsed.reviewBoundary(now), nil
}

// InAvoidance 报告 t 是否落在每小时的避让区间 :58:00～:04:00（UTC 分钟 ≥ 58 或 < 4）。
func InAvoidance(t time.Time) bool {
	minute := t.UTC().Minute()
	return minute >= avoidFromMinute || minute < avoidUntilMinute
}

// PullAt 返回边界 b 实际被拉取的时刻：b 之后（含）第一个检查时刻（第 25 秒），落在避让区间的顺延到 :04:25。
func PullAt(b time.Time) time.Time {
	b = b.UTC()
	at := b.Truncate(time.Minute).Add(CheckSecond * time.Second)
	if at.Before(b) {
		at = at.Add(time.Minute)
	}
	if InAvoidance(at) {
		hour := at.Truncate(time.Hour)
		if at.Minute() >= avoidFromMinute {
			hour = hour.Add(time.Hour)
		}
		at = hour.Add(avoidUntilMinute*time.Minute + CheckSecond*time.Second)
	}
	return at
}

// pullsAfter 返回严格晚于 after 的 count 个拉取时刻（去重：避让区间里的多个边界顺延到同一时刻）。
func (t timing) pullsAfter(after time.Time, count int) []time.Time {
	out := make([]time.Time, 0, count)
	for b, i := t.boundary(after), 0; len(out) < count && i < maxEnumerate; b, i = t.next(b), i+1 {
		at := PullAt(b)
		if !at.After(after) || (len(out) > 0 && !at.After(out[len(out)-1])) {
			continue
		}
		out = append(out, at)
	}
	return out
}

// reviewsAfter 返回严格晚于 after 的 count 个复核时刻；1d 及以上的复核就是拉取。
func (t timing) reviewsAfter(after time.Time, count int) []time.Time {
	if t.daily {
		return t.pullsAfter(after, count)
	}
	out := make([]time.Time, 0, count)
	for r := t.reviewBoundary(after); len(out) < count; r = r.Add(time.Hour) {
		if at := PullAt(r); at.After(after) {
			out = append(out, at)
		}
	}
	return out
}

// NextPulls 返回 now 之后的 count 个拉取时刻（1d 及以上即复核时刻）。
func NextPulls(t Timing, now time.Time, count int) ([]time.Time, error) {
	parsed, err := t.parse()
	if err != nil {
		return nil, err
	}
	return parsed.pullsAfter(now, count), nil
}

// NextReviews 返回 now 之后的 count 个复核时刻。
func NextReviews(t Timing, now time.Time, count int) ([]time.Time, error) {
	parsed, err := t.parse()
	if err != nil {
		return nil, err
	}
	return parsed.reviewsAfter(now, count), nil
}

// FirstRunAtOrAfter 返回不早于 at 的第一次拉取或复核时刻（预计导出时间用：到点拉取与复核都会导出到期的分区）。
func FirstRunAtOrAfter(t Timing, at time.Time) (time.Time, error) {
	parsed, err := t.parse()
	if err != nil {
		return time.Time{}, err
	}
	after := at.Add(-time.Nanosecond)
	var first time.Time
	for _, list := range [][]time.Time{parsed.pullsAfter(after, 1), parsed.reviewsAfter(after, 1)} {
		if len(list) > 0 && (first.IsZero() || list[0].Before(first)) {
			first = list[0]
		}
	}
	if first.IsZero() {
		return time.Time{}, errors.New("算不出下一次拉取时刻")
	}
	return first, nil
}

// Decision 是一次检查对一个任务的判定结果。
type Decision struct {
	Action Action
	// RangeStart、RangeEnd 是到点拉取的读取区间 [起点, now)；复核不带区间。
	RangeStart time.Time
	RangeEnd   time.Time
	// LastBoundary、LastReview 是生成运行后要记下的两个边界。
	LastBoundary time.Time
	LastReview   time.Time
}

// Decide 按任务记下的两个边界判定这次检查要做什么（不考虑已有运行：有排队中或运行中的运行时由调用方跳过、边界不前进）。
//   - 落在避让区间：什么都不做；
//   - 1d 及以上：拉取边界前进就生成复核，两个边界都记成这个边界；
//   - 低于 1d：复核边界前进就生成复核（拉取边界也到点时一并记下，复核已覆盖）；否则拉取边界前进就生成到点拉取，
//     读 [上次边界 − 2 根, now)，上次边界为空或比本次早 60 根以上时读 [本次边界 − (N + 2) 根, now)。
func Decide(t Timing, lastBoundary, lastReview, now time.Time) (Decision, error) {
	parsed, err := t.parse()
	if err != nil {
		return Decision{}, err
	}
	now = now.UTC()
	if InAvoidance(now) {
		return Decision{}, nil
	}
	b := parsed.boundary(now)
	tickDue := lastBoundary.IsZero() || b.After(lastBoundary)
	if parsed.daily {
		if !tickDue {
			return Decision{}, nil
		}
		return Decision{Action: ActionReview, LastBoundary: b, LastReview: b}, nil
	}
	r := parsed.reviewBoundary(now)
	reviewDue := lastReview.IsZero() || r.After(lastReview)
	switch {
	case reviewDue:
		out := Decision{Action: ActionReview, LastBoundary: lastBoundary, LastReview: r}
		if tickDue {
			out.LastBoundary = b
		}
		return out, nil
	case tickDue:
		start := lastBoundary.Add(-TickExtraBars * parsed.bar)
		if lastBoundary.IsZero() || lastBoundary.Before(b.Add(-tickMaxBehindBars*parsed.bar)) {
			start = b.Add(-time.Duration(parsed.n+TickExtraBars) * parsed.bar)
		}
		return Decision{Action: ActionTick, RangeStart: start, RangeEnd: now, LastBoundary: b, LastReview: lastReview}, nil
	}
	return Decision{}, nil
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

// ReadSpan 返回一次复核读取区间的长度：复核间隔 + 最少回看 + 一根 K 线的最大跨度。
func ReadSpan(interval, lookback time.Duration, freq string) (time.Duration, error) {
	span, err := BarSpan(freq)
	if err != nil {
		return 0, err
	}
	return interval + lookback + span, nil
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

// ExportEstimate 返回分区预计的导出时间：不早于「收盘时间 + 最少回看」的第一次拉取或复核。
func ExportEstimate(t Timing, close time.Time, lookback time.Duration) (time.Time, error) {
	return FirstRunAtOrAfter(t, close.Add(lookback))
}
