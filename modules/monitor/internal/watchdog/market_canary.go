package watchdog

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type MarketBar struct {
	DataTime time.Time
	Close    float64
	Volume   float64
	Closed   bool
}

type MarketCanaryConfig struct {
	Freshness            time.Duration
	ReturnThreshold      float64
	VolumeRatioThreshold float64
}

type MarketCanaryResult struct {
	Fresh       bool
	Abnormal    bool
	Return      float64
	VolumeRatio float64
	Watermark   time.Time
	Reason      string
}

func EvaluateMarketCanary(now time.Time, bars []MarketBar, cfg MarketCanaryConfig) (MarketCanaryResult, error) {
	if cfg.Freshness <= 0 || cfg.ReturnThreshold <= 0 || cfg.VolumeRatioThreshold <= 0 {
		return MarketCanaryResult{}, fmt.Errorf("market canary thresholds must be positive")
	}
	closed := make([]MarketBar, 0, len(bars))
	for _, bar := range bars {
		if !bar.Closed || bar.DataTime.IsZero() {
			continue
		}
		if bar.Close <= 0 || bar.Volume < 0 || math.IsNaN(bar.Close) || math.IsNaN(bar.Volume) ||
			math.IsInf(bar.Close, 0) || math.IsInf(bar.Volume, 0) {
			return MarketCanaryResult{}, fmt.Errorf("market canary bar contains invalid values")
		}
		closed = append(closed, bar)
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].DataTime.Before(closed[j].DataTime) })
	if len(closed) < 2 {
		return MarketCanaryResult{Reason: "insufficient_closed_bars"}, nil
	}
	previous, current := closed[len(closed)-2], closed[len(closed)-1]
	result := MarketCanaryResult{Watermark: current.DataTime.UTC()}
	if now.UTC().Sub(current.DataTime.UTC()) > cfg.Freshness {
		result.Reason = "stale_watermark"
		return result, nil
	}
	result.Fresh = true
	result.Return = math.Abs(current.Close/previous.Close - 1)
	result.VolumeRatio = current.Volume / math.Max(previous.Volume, 1e-12)
	result.Abnormal = result.Return >= cfg.ReturnThreshold || result.VolumeRatio >= cfg.VolumeRatioThreshold
	if result.Abnormal {
		result.Reason = "threshold_exceeded"
	} else {
		result.Reason = "ok"
	}
	return result, nil
}
