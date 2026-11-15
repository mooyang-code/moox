package healthz

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 上报用的就绪判断与 /readyz 同一个函数：组件写了原因就用它，否则从 Details 里列出没通过的项；还没 SetReady 时说明启动尚未完成。
func TestReadinessUsesTheReadyzSnapshot(t *testing.T) {
	ready, reason := Readiness(func(context.Context) Response {
		return Response{Ready: false, Reason: "数据库不可用"}
	})(t.Context())
	require.False(t, ready)
	require.Equal(t, "数据库不可用", reason)

	_, reason = Readiness(func(context.Context) Response {
		return Response{Details: map[string]any{"database_ready": false, "eventbus_connected": true, "outbox_stalled": false, "catalog_sync_error": "超时"}}
	})(t.Context())
	require.Equal(t, "未通过：catalog_sync_error=超时，database_ready", reason)

	state := NewState("trade", "trade", "", "")
	ready, reason = Readiness(state.Snapshot)(t.Context())
	require.False(t, ready)
	require.Equal(t, "启动尚未完成", reason)
	state.SetReady(true)
	ready, reason = Readiness(state.Snapshot)(t.Context())
	require.True(t, ready)
	require.Empty(t, reason)

	ready, _ = Readiness(func(context.Context) Response { panic("boom") })(t.Context())
	require.False(t, ready, "快照函数出错按未就绪")
}
