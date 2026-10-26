package observability

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLifecycleFromHealthBody(t *testing.T) {
	body := `{"module":"strategy","ready":true,"details":{"outbox_pending_count":0,"lifecycle":{"stage":"running","color":"green","degraded":true,"self":"ready","counts":{"running":2,"paused":1},"attention":[{"kind":"strategy_instance","id":"i1","stage":"paused","reason":"实例健康：degraded","checks":[{"name":"实例健康","message":"实例处于 degraded","remedy":"重新启用实例"}]}],"recovered":1}}}`
	l := ParseLifecycle(body)
	require.NotNil(t, l)
	require.Equal(t, "running", l.Stage)
	require.True(t, l.Degraded)
	require.Equal(t, 2, l.Counts["running"])
	require.Equal(t, "i1", l.Attention[0].ID)
	require.Equal(t, "重新启用实例", l.Attention[0].Checks[0].Remedy)

	require.Nil(t, ParseLifecycle(""))
	require.Nil(t, ParseLifecycle(`{"module":"storage","details":{}}`), "模块没有生命周期接口")
	require.Nil(t, ParseLifecycle(`{"details":{"lifecycle":{"stage":"run`), "响应体被截断时不报错、不显示")
}

func TestParseLifecycleReadsPauseHoldAndCatchup(t *testing.T) {
	body := `{"status":"ok","details":{"lifecycle":{"stage":"resuming","color":"cyan","self":"resuming","hold":"system_pause",
		"pause":{"kind":"system","reason":"切换行情源","by":"cli:ops","since":"2026-10-21T10:00:00Z"},
		"catchup":{"since":"2026-10-21T10:00:00Z","done":6,"total":10,"caught_up":false,"blocked_by":"collector","unfillable":2},
		"pause_kinds":{"manual":1},
		"attention":[{"kind":"factor_task","id":"s1","stage":"paused","effective":"paused","pause":{"kind":"manual","reason":"手动暂停"}}]}}}`
	got := ParseLifecycle(body)
	require.NotNil(t, got)
	require.Equal(t, "system_pause", got.Hold)
	require.Equal(t, "system", got.Pause.Kind)
	require.Equal(t, "cli:ops", got.Pause.By)
	require.Equal(t, int64(6), got.Catchup.Done)
	require.Equal(t, "collector", got.Catchup.BlockedBy)
	require.Equal(t, 2, got.Catchup.Unfillable)
	require.Equal(t, 1, got.PauseKinds["manual"])
	require.Equal(t, "manual", got.Attention[0].Pause.Kind)
	require.Equal(t, "paused", got.Attention[0].Effective)
}
