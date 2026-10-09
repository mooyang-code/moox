package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func setSCFAccessEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("MOOX_CALLER", "scf-collector")
	t.Setenv("MOOX_CALLER_KEY", "scf-collector-1:scf-access-test-secret")
	t.Setenv("MOOX_ACCESS_ADDRESS", "10.206.0.5:11004")
	t.Setenv("MOOX_ACCESS_ID", "access@storage")
}

func TestSCFGatewayFollowsFunctionEnvironment(t *testing.T) {
	setSCFAccessEnvironment(t)
	first, err := scfGateway()
	require.NoError(t, err)
	require.Equal(t, "scf-collector", first.Caller())
	again, err := scfGateway()
	require.NoError(t, err)
	require.Same(t, first, again, "函数实例内复用同一个客户端")

	t.Setenv("MOOX_ACCESS_ADDRESS", "146.56.196.204:11004")
	changed, err := scfGateway()
	require.NoError(t, err)
	require.NotSame(t, first, changed, "外部接入地址变化后重新创建客户端")

	t.Setenv("MOOX_CALLER_KEY", "")
	_, err = scfGateway()
	require.ErrorContains(t, err, "MOOX_CALLER_KEY")
}

func TestHandleTimerReportsMissingAccessEnvironment(t *testing.T) {
	setTimerClaimEnvironment(t)
	setSCFAccessEnvironment(t)
	t.Setenv("MOOX_ACCESS_ID", "storage")
	response, err := NewHandler().HandleTimerAt(context.Background(), "request-1", "function-1", time.Now())
	require.NoError(t, err)
	require.False(t, response.Success)
	require.Contains(t, response.Message, "access@", "外部接入环境变量不完整时直接失败，不发出任何请求")
}
