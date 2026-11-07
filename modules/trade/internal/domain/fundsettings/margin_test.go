package fundsettings

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 没写保证金率监控的设置时取默认值：普通账户 0.5、统一账户 1.5、减仓 10%，监控开启。
func TestMarginMonitorDefaults(t *testing.T) {
	for _, raw := range []string{"", "{}"} {
		settings, warning, err := ParseLenient(raw)
		require.NoError(t, err)
		require.NoError(t, warning)
		require.Equal(t, "0.5", settings.MarginRatioThreshold.String())
		require.Equal(t, "1.5", settings.UnifiedMMRThreshold.String())
		require.Equal(t, "0.1", settings.MarginReduceRatio.String())
		require.False(t, settings.MarginMonitorDisabled)
	}
	settings, _, _ := ParseLenient("")
	require.Equal(t, "0.5", settings.Document().MarginRatioThreshold, "界面看到当前生效的值")
}

func TestMarginMonitorWriteValidatesRanges(t *testing.T) {
	good := `{"margin_ratio_threshold":"0.6","unified_mmr_threshold":"2","margin_reduce_ratio":"0.2","margin_monitor_disabled":true}`
	settings, canonical, err := parseForWrite(good, "USDT")
	require.NoError(t, err)
	require.Equal(t, "0.6", settings.MarginRatioThreshold.String())
	require.Equal(t, "2", settings.UnifiedMMRThreshold.String())
	require.Equal(t, "0.2", settings.MarginReduceRatio.String())
	require.True(t, settings.MarginMonitorDisabled)
	require.JSONEq(t, good, canonical)

	// 边界：普通账户 (0, 10]；统一账户 (1, 100]；减仓比例 (0, 1)。
	for raw, valid := range map[string]bool{
		`{"margin_ratio_threshold":"10"}`: true, `{"margin_ratio_threshold":"10.1"}`: false, `{"margin_ratio_threshold":"0"}`: false, `{"margin_ratio_threshold":"-1"}`: false,
		`{"unified_mmr_threshold":"1.01"}`: true, `{"unified_mmr_threshold":"1"}`: false, `{"unified_mmr_threshold":"100"}`: true, `{"unified_mmr_threshold":"101"}`: false,
		`{"margin_reduce_ratio":"0.99"}`: true, `{"margin_reduce_ratio":"1"}`: false, `{"margin_reduce_ratio":"0"}`: false, `{"margin_reduce_ratio":"abc"}`: false,
		`{"margin_ratio_threshold":""}`: true,
	} {
		_, _, err := parseForWrite(raw, "USDT")
		require.Equal(t, valid, err == nil, raw)
	}
	_, _, err = parseForWrite(`{"margin_monitor_disabled":"yes"}`, "USDT")
	require.Error(t, err, "开关给字符串")
}

// 读取路径：数值坏了取默认值并给 warning；关闭开关读不出来时按开启监控处理。
func TestMarginMonitorLenientFallsBackToSafeDefaults(t *testing.T) {
	settings, warning, err := ParseLenient(`{"margin_ratio_threshold":"abc","unified_mmr_threshold":"0.5","margin_reduce_ratio":"2","margin_monitor_disabled":"yes"}`)
	require.NoError(t, err)
	require.Error(t, warning)
	require.Equal(t, "0.5", settings.MarginRatioThreshold.String())
	require.Equal(t, "1.5", settings.UnifiedMMRThreshold.String(), "不合法的阈值取默认值，不是 0.5")
	require.Equal(t, "0.1", settings.MarginReduceRatio.String())
	require.False(t, settings.MarginMonitorDisabled, "监控是保护，读不出来的关闭开关不能悄悄把监控关掉")

	settings, warning, err = ParseLenient(`{"margin_ratio_threshold":"0.7","margin_monitor_disabled":true}`)
	require.NoError(t, err)
	require.NoError(t, warning)
	require.Equal(t, "0.7", settings.MarginRatioThreshold.String())
	require.True(t, settings.MarginMonitorDisabled)
}
