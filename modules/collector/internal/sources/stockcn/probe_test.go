package stockcn

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const probeFixture = "../../../testdata/stockcn/probe_contract.json"

func TestProbeReportStrictJSONContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(probeFixture))
	require.NoError(t, err)

	report, err := DecodeProbeReport(bytes.NewReader(raw))
	require.NoError(t, err)

	require.Equal(t, "stockcn", report.MarketID)
	require.Equal(t, "1m", report.Frequency)
	require.Len(t, report.Entries, 8)

	byKey := make(map[string]ProbeEntry, len(report.Entries))
	for _, entry := range report.Entries {
		byKey[entry.ProviderID+":"+string(entry.FeedKind)] = entry
		assert.NotContains(t, entry.Error, "Cookie")
		assert.NotContains(t, entry.Error, "Header")
	}

	assert.Equal(t, ProbeResultPass, byKey["sina:kline"].Result)
	assert.Equal(t, ProbeResultPass, byKey["tencent:kline"].Result)
	assert.Equal(t, ProbeResultPass, byKey["eastmoney:kline"].Result)
	assert.Equal(t, ProbeResultShadowOnly, byKey["baidu:kline"].Result)
	assert.Equal(t, ProbeResultNotSupported, byKey["sina:instrument"].Result)
	assert.Equal(t, ProbeResultNotSupported, byKey["baidu:instrument"].Result)
	assert.True(t, byKey["sina:kline"].HasOHLCV)
	assert.False(t, byKey["baidu:kline"].HasOHLCV)
	assert.Equal(t, "shares", byKey["sina:kline"].VolumeUnit)
}

func TestProbeReportMarkdownRender(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(probeFixture))
	require.NoError(t, err)

	report, err := DecodeProbeReport(bytes.NewReader(raw))
	require.NoError(t, err)

	markdown := report.RenderMarkdown()
	assert.Contains(t, markdown, "# stockcn Provider Probe")
	assert.Contains(t, markdown, "| sina | kline | PASS |")
	assert.Contains(t, markdown, "| baidu | kline | SHADOW_ONLY |")
	assert.Contains(t, markdown, "| sina | instrument | NOT_SUPPORTED |")
	assert.NotContains(t, markdown, "Cookie")
	assert.NotContains(t, markdown, "Header")
}

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
