package stockcn

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeReportStrictJSONContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "probe_contract.json"))
	require.NoError(t, err)

	report, err := DecodeProbeReport(bytes.NewReader(raw))
	require.NoError(t, err)

	require.Equal(t, "stock_cn", report.MarketID)
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
	raw, err := os.ReadFile(filepath.Join("testdata", "probe_contract.json"))
	require.NoError(t, err)

	report, err := DecodeProbeReport(bytes.NewReader(raw))
	require.NoError(t, err)

	markdown := report.RenderMarkdown()
	assert.Contains(t, markdown, "# stock_cn Provider Probe")
	assert.Contains(t, markdown, "| sina | kline | PASS |")
	assert.Contains(t, markdown, "| baidu | kline | SHADOW_ONLY |")
	assert.Contains(t, markdown, "| sina | instrument | NOT_SUPPORTED |")
	assert.NotContains(t, markdown, "Cookie")
	assert.NotContains(t, markdown, "Header")
}
