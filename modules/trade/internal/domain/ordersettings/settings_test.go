package ordersettings

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

func TestDefaultsMatchThePlan(t *testing.T) {
	for _, raw := range []string{"", "{}"} {
		settings, err := Parse(raw)
		require.NoError(t, err)
		require.Equal(t, ThresholdTargetRatio, settings.ThresholdMode)
		require.Equal(t, "0.05", settings.ThresholdRatio.String())
		require.Equal(t, "10", settings.MinNotionalSpot.String())
		require.Equal(t, "5", settings.MinNotionalSwap.String())
		require.False(t, settings.HasHourlyCaps())
		require.Equal(t, "100000", settings.MaxChildNotional.String())
		require.False(t, settings.RandomizeSplit)
		require.False(t, settings.BatchIntervalSet)
		require.Equal(t, OrderModePseudoMarket, settings.OrderMode)
		require.Equal(t, "0.015", settings.SlippageStep.String())
		require.Equal(t, "0.06", settings.SlippageMax.String())
		require.Equal(t, 4, settings.MaxAttempts)
		require.True(t, settings.RestingRatio.IsZero())
		require.Zero(t, settings.RestingTTL)
	}
}

func TestSlippageGrowsByStepUntilCeiling(t *testing.T) {
	settings := Default()
	for attempt, want := range []string{"0.015", "0.03", "0.045", "0.06", "0.06"} {
		require.Equal(t, want, settings.Slippage(attempt).String(), attempt)
	}
}

func TestParseRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{
		`not json`, `[]`,
		`{"resting_ratio":"1.1"}`, `{"resting_ratio":"-0.1"}`, `{"resting_ratio":"abc"}`,
		`{"rebalance_threshold_mode":"SOMETIMES"}`, `{"rebalance_threshold_ratio":"1.5"}`,
		`{"min_notional_spot":"-1"}`, `{"max_child_notional":"0"}`, `{"max_child_notional":"1000001"}`,
		`{"batch_interval_seconds":-1}`, `{"batch_interval_seconds":3601}`,
		`{"order_mode":"LIMIT"}`, `{"slippage_step":"0"}`, `{"slippage_step":"0.5"}`,
		`{"slippage_step":"0.02","slippage_max":"0.01"}`, `{"max_attempts":-1}`, `{"max_attempts":11}`,
		`{"resting_ttl_seconds":-5}`, `{"hourly_cap_spot":{"BTC-USDT-SWAP":"100"}}`,
		`{"hourly_cap_swap":{"BTC":"100"}}`, `{"hourly_cap_spot":{"BTC-USDT-SPOT":"0"}}`,
		`{"hourly_cap_spot":{"btc-usdt-spot":"1","BTC-USDT-SPOT":"2"}}`,
		`{"randomize_split":"yes"}`, `{"max_attempts":"4"}`,
	} {
		_, err := Parse(raw)
		require.Error(t, err, raw)
	}
}

func TestParseRejectsUnknownAndMiscasedKeys(t *testing.T) {
	_, err := Parse(`{"resting_ratio":"0.5","unknown_key":1}`)
	require.ErrorContains(t, err, "未知")
	_, err = Parse(`{"Resting_Ratio":"0.5"}`)
	require.ErrorContains(t, err, "大小写")
}

func TestHourlyCapLookupFallsBackToWildcard(t *testing.T) {
	settings, err := Parse(`{"hourly_cap_spot":{"btc-usdt-spot":"500","*":"200"},"hourly_cap_swap":{"*":"1000"}}`)
	require.NoError(t, err)
	limit, ok := settings.HourlyCap("SPOT", "BTC-USDT-SPOT")
	require.True(t, ok)
	require.Equal(t, "500", limit.String())
	limit, ok = settings.HourlyCap("SPOT", "ETH-USDT-SPOT")
	require.True(t, ok)
	require.Equal(t, "200", limit.String())
	limit, ok = settings.HourlyCap("SWAP", "BTC-USDT-SWAP")
	require.True(t, ok)
	require.Equal(t, "1000", limit.String())
	_, ok = Default().HourlyCap("SPOT", "BTC-USDT-SPOT")
	require.False(t, ok, "没配置就是不限")
}

func TestCanonicalRoundTripsAndNormalizesKeys(t *testing.T) {
	var document Document
	require.NoError(t, shared.DecodeStrictObject(`{
		"rebalance_threshold_mode":"EQUITY_RATIO","rebalance_threshold_ratio":"0.02",
		"hourly_cap_swap":{"btc-usdt-swap":"1000.50"},"batch_interval_seconds":3,"resting_ratio":"0.3","resting_ttl_seconds":600}`, &document))
	canonical, err := document.Canonical()
	require.NoError(t, err)
	require.JSONEq(t, `{"rebalance_threshold_mode":"EQUITY_RATIO","rebalance_threshold_ratio":"0.02",
		"hourly_cap_swap":{"BTC-USDT-SWAP":"1000.5"},"batch_interval_seconds":3,"resting_ratio":"0.3","resting_ttl_seconds":600}`, canonical)
	settings, err := Parse(canonical)
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, settings.BatchInterval)
	require.True(t, settings.BatchIntervalSet)
	require.Equal(t, 10*time.Minute, settings.RestingTTL)
	// 有效设置转回文档再校验，结论不变（界面展示后原样提交也能通过）。
	again, err := settings.Document().Validate()
	require.NoError(t, err)
	require.Equal(t, settings.MaxChildNotional.String(), again.MaxChildNotional.String())
	require.Equal(t, settings.OrderMode, again.OrderMode)
}

// 小数写进库前规范化："0.050" 存成 "0.05"，没写的项不写出来。
func TestCanonicalNormalizesDecimals(t *testing.T) {
	var document Document
	require.NoError(t, shared.DecodeStrictObject(`{"rebalance_threshold_ratio":"0.050","min_notional_spot":"10.00","slippage_step":"0.0150","resting_ratio":"0.30"}`, &document))
	canonical, err := document.Canonical()
	require.NoError(t, err)
	require.JSONEq(t, `{"rebalance_threshold_ratio":"0.05","min_notional_spot":"10","slippage_step":"0.015","resting_ratio":"0.3"}`, canonical)
}
