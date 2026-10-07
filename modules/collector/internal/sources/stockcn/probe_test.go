package stockcn

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeReportRetainsHistoryAndRateEvidence(t *testing.T) {
	report := ProbeReport{
		MarketID: "stockcn", Frequency: "1m", GeneratedAt: "2026-08-30T00:00:00Z", Subjects: []string{"600000.XSHG"},
		Entries: []ProbeEntry{{
			ProviderID: "sina", FeedKind: ProbeFeedKline, Exchange: "XSHG", SubjectID: "600000.XSHG", Symbol: "sh600000",
			Result: ProbeResultPass, ErrorKind: "none", VolumeUnit: "shares", AmountUnit: "cny", BarCount: 3,
			HasOHLCV: true, History: HistoryProbe{RequestedStart: "2026-08-29T00:00:00Z", Start: "2026-08-29T06:59:00Z", Result: ProbeResultPass, BarCount: 10},
			Rate: RateProbe{Concurrency: []int{1, 2, 4}, Requests: 7, RateLimited: 1, P95LatencyMS: 120, ObservedRequestsPerSec: 4.2},
		}},
	}
	raw, err := report.MarshalJSONStrict()
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"history"`)
	assert.Contains(t, string(raw), `"rate"`)
	markdown := report.RenderMarkdown()
	assert.Contains(t, markdown, "history=PASS")
	assert.Contains(t, markdown, "rate_requests=7")
}
