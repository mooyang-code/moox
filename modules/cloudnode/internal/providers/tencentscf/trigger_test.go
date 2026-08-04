package tencentscf

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
)

func TestTimerTriggerInfoNormalizesProviderCronAndState(t *testing.T) {
	item := &scf.TriggerInfo{
		TriggerName:     common.StringPtr("moox-market-fetch-timer"),
		Type:            common.StringPtr("timer"),
		TriggerDesc:     common.StringPtr(`{"cron":"0 * * * * * *"}`),
		Enable:          common.Uint64Ptr(1),
		AvailableStatus: common.StringPtr("Available"),
		Qualifier:       common.StringPtr("$LATEST"),
		CustomArgument:  common.StringPtr("market_fetch_timer_v1"),
	}
	info := timerTriggerInfoFromSDK(item)
	require.Equal(t, "moox-market-fetch-timer", info.Name)
	require.Equal(t, "timer", info.Type)
	require.Equal(t, "0 * * * * * *", info.Cron)
	require.True(t, info.Enabled)
	require.Equal(t, "Available", info.AvailableStatus)
	require.Equal(t, "$LATEST", info.Qualifier)
	require.Equal(t, "market_fetch_timer_v1", info.Message)
}

func TestTimerTriggerHelpersUseTencentContract(t *testing.T) {
	require.Equal(t, "OPEN", timerEnable(true))
	require.Equal(t, "CLOSE", timerEnable(false))
	require.Equal(t, "0 * * * * * *", normalizeTimerCron(common.StringPtr("0 * * * * * *")))
	require.Equal(t, "0 */5 * * * * *", normalizeTimerCron(common.StringPtr(`{"cron":"0 */5 * * * * *"}`)))
	require.Equal(t, "$LATEST", firstNonEmpty("", " $LATEST "))
}

func TestValidateTimerTriggerInfoRejectsStaleOrUnavailableReadback(t *testing.T) {
	req := TimerTriggerRequest{Name: "timer", Cron: "0 * * * * * *", Enabled: true, Qualifier: "$LATEST", Message: "market_fetch_timer_v1"}
	base := &TimerTriggerInfo{Name: req.Name, Type: "timer", Cron: req.Cron, Enabled: true, AvailableStatus: "Available", Qualifier: req.Qualifier, Message: req.Message}
	require.NoError(t, validateTimerTriggerInfo(base, req, req.Qualifier, req.Message))
	base.AvailableStatus = "Updating"
	require.ErrorContains(t, validateTimerTriggerInfo(base, req, req.Qualifier, req.Message), "not available")
	base.AvailableStatus = "Available"
	base.Cron = "0 */5 * * * * *"
	require.ErrorContains(t, validateTimerTriggerInfo(base, req, req.Qualifier, req.Message), "does not match")
}
