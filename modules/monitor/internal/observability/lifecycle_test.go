package observability

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLifecycleFromHealthBody(t *testing.T) {
	body := `{"module":"strategy","ready":true,"details":{"outbox_pending_count":0,"lifecycle":{"module":"strategy","stage":"running","since":"2026-10-21T10:00:00Z"}}}`
	l := ParseLifecycle(body)
	require.NotNil(t, l)
	require.Equal(t, "running", l.Stage)
	require.Nil(t, l.Pause)

	require.Nil(t, ParseLifecycle(""))
	require.Nil(t, ParseLifecycle(`{"module":"storage","details":{}}`), "模块没有生命周期接口")
	require.Nil(t, ParseLifecycle(`{"details":{"lifecycle":{"stage":"run`), "响应体被截断时不报错、不显示")
}

func TestParseLifecycleReadsPauseAndCatchup(t *testing.T) {
	body := `{"status":"ok","details":{"lifecycle":{"module":"collector","stage":"resuming","reason":"恢复追赶",
		"pause":{"kind":"system","reason":"切换行情源","by":"cli:ops","since":"2026-10-21T10:00:00Z"},
		"catchup":{"since":"2026-10-21T10:00:00Z","done":6,"total":10,"caught_up":false,"blocked_by":"collector","unfillable":2}}}}`
	got := ParseLifecycle(body)
	require.NotNil(t, got)
	require.Equal(t, "resuming", got.Stage)
	require.Equal(t, "恢复追赶", got.Reason)
	require.Equal(t, "system", got.Pause.Kind)
	require.Equal(t, "cli:ops", got.Pause.By)
	require.Equal(t, int64(6), got.Catchup.Done)
	require.Equal(t, "collector", got.Catchup.BlockedBy)
	require.Equal(t, 2, got.Catchup.Unfillable)
}
