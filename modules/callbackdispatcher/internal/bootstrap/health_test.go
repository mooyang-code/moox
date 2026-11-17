package bootstrap

import (
	"testing"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/health"
	"github.com/stretchr/testify/require"
)

// 就绪位：数据库可用并且事件消费者没有长时间停着才算就绪；收不到事件时说明写进详情。
func TestHealthSnapshotNotReadyWhenConsumersStalled(t *testing.T) {
	state := health.New("callback-dispatcher", "callback-dispatcher", "", "")

	ok := healthSnapshot(state, true, true, nil, "")
	require.True(t, ok.Ready)
	require.True(t, state.Ready())
	require.NotContains(t, ok.Details, "consumer_error")

	stalled := healthSnapshot(state, true, false, nil, "事件 x 的消费者已有 4m0s 没有运行")
	require.False(t, stalled.Ready)
	require.False(t, state.Ready())
	require.Equal(t, "degraded", stalled.Status)
	require.Equal(t, "事件 x 的消费者已有 4m0s 没有运行", stalled.Details["consumer_error"])

	require.False(t, healthSnapshot(state, false, true, nil, "").Ready, "数据库不可用同样未就绪")
}
