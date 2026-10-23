// Package barclock 把「K 线桶」的切分方式集中在一处：合成按它分桶，Monitor 按它判断应收的 K 线。
//
// 两种分桶方式：
//   - epoch_utc：24 小时连续市场（加密货币），从 Unix 零点起按周期切分，1d 为 UTC 自然日；
//   - session：交易时段市场（A 股），每个交易日的每个交易时段从开盘起按周期切分，不跨午休，不截短，
//     休市日没有桶；1d 为交易日，一天一根（桶的键是交易日期的 UTC 零点，区间是首个时段开盘到末个时段收盘）。
//
// session 的日内周期必须整除每个交易时段的长度，除不尽的周期在构造时直接拒绝。
package barclock

import (
	"errors"
	"time"
)

const (
	// AlignmentEpochUTC 是 24 小时连续市场的分桶方式。
	AlignmentEpochUTC = "epoch_utc"
	// AlignmentSession 是交易时段市场的分桶方式。
	AlignmentSession = "session"
)

var (
	// ErrNoCalendar 表示市场没有可用的交易日历，无法按交易时段分桶。
	ErrNoCalendar = errors.New("市场没有交易日历")
	// ErrUnknownMarket 表示市场未登记分桶方式。
	ErrUnknownMarket = errors.New("未知的市场")
	// ErrOutOfCoverage 表示用到的日期超出了交易日历的覆盖范围：不推测是否交易日。
	ErrOutOfCoverage = errors.New("日期超出交易日历的覆盖范围")
	// ErrNoBucket 表示在查找范围内没有符合条件的桶。
	ErrNoBucket = errors.New("没有符合条件的桶")
	// ErrPeriodNotDivisible 表示周期不能整除交易时段（或源周期不能整除目标周期）。
	ErrPeriodNotDivisible = errors.New("周期不能整除")
)

// Bucket 是一个目标 K 线桶。
type Bucket struct {
	// Key 是桶在存储里的 data_time：日内桶是开盘时刻；session 的 1d 桶是交易日期的 UTC 零点。
	Key time.Time
	// Start、End 是桶覆盖的时间区间 [Start, End)，源 K 线按它选取。
	Start, End time.Time
}

// Clock 切分 K 线桶。所有时间都是 UTC 时刻，实现内部按市场时区换算。
type Clock interface {
	Alignment() string
	Period() time.Duration
	// BucketAt 返回包含 t 的桶；t 不在任何桶内（休市、午休、非交易日）时 ok 为假。
	BucketAt(t time.Time) (b Bucket, ok bool, err error)
	// LatestClosed 返回收盘时刻不晚于 now 的最新一个桶。
	LatestClosed(now time.Time) (Bucket, error)
	// ClosedBefore 返回收盘时刻不晚于 now 的最近 n 个桶，按时间升序。
	ClosedBefore(now time.Time, n int) ([]Bucket, error)
	// Next、Prev 返回紧随、紧邻其前的桶。
	Next(b Bucket) (Bucket, error)
	Prev(b Bucket) (Bucket, error)
	// Between 返回与 [start, end) 有交集的桶，按时间升序，最多 limit 个。
	Between(start, end time.Time, limit int) ([]Bucket, error)
	// ValidateSource 检查 source 能否整除桶的构成（目标周期与每个交易时段）。
	ValidateSource(source time.Duration) error
	// SourceTimes 返回桶内应有的源 K 线开盘时刻（升序），不含午休与休市。
	SourceTimes(b Bucket, source time.Duration) ([]time.Time, error)
}

func closedBefore(c Clock, now time.Time, n int) ([]Bucket, error) {
	if n <= 0 {
		return nil, nil
	}
	latest, err := c.LatestClosed(now)
	if err != nil {
		return nil, err
	}
	out := make([]Bucket, n)
	out[n-1] = latest
	for i := n - 2; i >= 0; i-- {
		prev, err := c.Prev(out[i+1])
		if err != nil {
			return out[i+1:], nil
		}
		out[i] = prev
	}
	return out, nil
}

// epochClock 从 Unix 零点起按固定周期切分。
type epochClock struct{ period time.Duration }

// NewEpoch 返回按 epoch UTC 切分的时钟。
func NewEpoch(period time.Duration) (Clock, error) {
	if period < time.Minute || period > 24*time.Hour || (24*time.Hour)%period != 0 {
		return nil, errors.New("epoch_utc 的周期必须在 1m～1d 且整除一天")
	}
	return epochClock{period: period}, nil
}

func (c epochClock) Alignment() string     { return AlignmentEpochUTC }
func (c epochClock) Period() time.Duration { return c.period }

func (c epochClock) bucket(start time.Time) Bucket {
	start = start.UTC()
	return Bucket{Key: start, Start: start, End: start.Add(c.period)}
}

func (c epochClock) floor(t time.Time) time.Time {
	seconds := t.UTC().Unix()
	step := int64(c.period / time.Second)
	rem := seconds % step
	if rem < 0 {
		rem += step
	}
	return time.Unix(seconds-rem, 0).UTC()
}

func (c epochClock) BucketAt(t time.Time) (Bucket, bool, error) {
	return c.bucket(c.floor(t)), true, nil
}

func (c epochClock) LatestClosed(now time.Time) (Bucket, error) {
	return c.bucket(c.floor(now).Add(-c.period)), nil
}

func (c epochClock) ClosedBefore(now time.Time, n int) ([]Bucket, error) {
	return closedBefore(c, now, n)
}

func (c epochClock) Next(b Bucket) (Bucket, error) { return c.bucket(b.Start.Add(c.period)), nil }
func (c epochClock) Prev(b Bucket) (Bucket, error) { return c.bucket(b.Start.Add(-c.period)), nil }

func (c epochClock) Between(start, end time.Time, limit int) ([]Bucket, error) {
	var out []Bucket
	for cursor := c.floor(start); cursor.Before(end) && len(out) < limit; cursor = cursor.Add(c.period) {
		out = append(out, c.bucket(cursor))
	}
	return out, nil
}

func (c epochClock) ValidateSource(source time.Duration) error {
	if source <= 0 || source > c.period || c.period%source != 0 {
		return ErrPeriodNotDivisible
	}
	return nil
}

func (c epochClock) SourceTimes(b Bucket, source time.Duration) ([]time.Time, error) {
	if err := c.ValidateSource(source); err != nil {
		return nil, err
	}
	count := int(b.End.Sub(b.Start) / source)
	out := make([]time.Time, count)
	for i := range out {
		out[i] = b.Start.Add(time.Duration(i) * source)
	}
	return out, nil
}
