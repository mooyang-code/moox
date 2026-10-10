package marketfetch

import (
	"testing"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

func TestTimerRequestFromEnvBuildsClaimWithoutUsingLegacyMembership(t *testing.T) {
	setTimerClaimEnvironment(t)
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "stale-subject")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"stale-subject":"STALE"}`)
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "stale-provider")

	invocation, err := TimerRequestFromEnv("request-1", "function-1", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, &collectorpb.ClaimTimerBatchReq{
		SpaceId: "stockcn", FunctionName: "function-1", RequestId: "request-1",
		GroupId: 3, GroupCount: 200, BindingHash: "binding-hash", TickTime: 1785805323,
	}, invocation.Claim)
	require.NotContains(t, invocation.Claim.String(), "stale-subject")
}

func TestTimerRequestFromEnvRequiresCompleteRuntimeIdentity(t *testing.T) {
	setTimerClaimEnvironment(t)
	_, err := TimerRequestFromEnv("", "function-1", time.Now())
	require.ErrorContains(t, err, "claim identity is incomplete")
}

func TestTimerRequestFromEnvRejectsOutOfRangeGroup(t *testing.T) {
	setTimerClaimEnvironment(t)
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "200")
	_, err := TimerRequestFromEnv("request-1", "function-1", time.Now())
	require.ErrorContains(t, err, "outside [0,200)")
}

func setTimerClaimEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("MOOX_SPACE_ID", "stockcn")
	t.Setenv("MOOX_SCF_FUNCTION_NAME", "")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "3")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "200")
	t.Setenv("MOOX_MARKET_FETCH_BINDING_HASH", "binding-hash")
	t.Setenv("MOOX_COLLECTOR_RPC_GATEWAY_TARGET", "runtime.local:11003")
	t.Setenv("MOOX_COLLECTOR_GATEWAY_TARGET_NODE", "collector-node")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "storage.local:11003")
	t.Setenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON", "")
}
