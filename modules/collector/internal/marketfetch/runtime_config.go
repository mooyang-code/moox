package marketfetch

import "time"

const KlineProviderAttemptBudget = klineProviderAttemptBudget

// attemptTimeoutFloor 是单次行情请求的最短超时，来自请求携带的 FetchLimits.RequestTimeoutMS；未设置时为 0（不设下限）。
func attemptTimeoutFloor(limits *FeedLimits) time.Duration {
	if limits == nil || limits.RequestTimeoutMS <= 0 {
		return 0
	}
	return time.Duration(limits.RequestTimeoutMS) * time.Millisecond
}
