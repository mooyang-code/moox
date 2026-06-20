package bench

import (
	"sort"
	"time"
)

type LatencyRecorder struct {
	values []time.Duration
}

type LatencySummary struct {
	Count int     `json:"count"`
	MinMS float64 `json:"min_ms"`
	P50MS float64 `json:"p50_ms"`
	P90MS float64 `json:"p90_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
	AvgMS float64 `json:"avg_ms"`
}

func (r *LatencyRecorder) Add(value time.Duration) {
	r.values = append(r.values, value)
}

func (r *LatencyRecorder) Summary() LatencySummary {
	if len(r.values) == 0 {
		return LatencySummary{}
	}
	values := append([]time.Duration(nil), r.values...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return LatencySummary{
		Count: len(values),
		MinMS: durationMS(values[0]),
		P50MS: durationMS(percentile(values, 0.50)),
		P90MS: durationMS(percentile(values, 0.90)),
		P95MS: durationMS(percentile(values, 0.95)),
		P99MS: durationMS(percentile(values, 0.99)),
		MaxMS: durationMS(values[len(values)-1]),
		AvgMS: durationMS(total / time.Duration(len(values))),
	}
}

func percentile(values []time.Duration, ratio float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(ratio*float64(len(values)-1) + 0.999999)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func durationMS(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}
