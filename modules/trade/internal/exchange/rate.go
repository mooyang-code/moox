package exchange

import "time"

// RateStatus 是交易所接口当前的限流状态，供限速、对账推迟和监控使用。
type RateStatus struct {
	UsedWeight        int
	WeightLimit       int
	PausedUntil       time.Time
	PauseReason       ErrorKind
	OrdersPausedUntil time.Time
}

// UsageRatio 是本分钟已用权重占额度的比例；没有权重模型时为 0。
func (s RateStatus) UsageRatio() float64 {
	if s.WeightLimit <= 0 {
		return 0
	}
	return float64(s.UsedWeight) / float64(s.WeightLimit)
}

// Paused 报告接口当前是否整体暂停。
func (s RateStatus) Paused(now time.Time) bool {
	return now.Before(s.PausedUntil)
}

// RateStatusSource 由真实交易所适配器实现；模拟盘没有限流。
type RateStatusSource interface {
	RateStatus() RateStatus
}
