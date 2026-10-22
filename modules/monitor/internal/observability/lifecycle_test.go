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
