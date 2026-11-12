package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"github.com/stretchr/testify/require"
)

// 各服务上报的一批日志写进库：主机取上报里的节点、组件取服务名；同一批重复投递不会重复入库；
// 进程重启（启动号不同）后同样的序号是新记录。
func TestLogObservabilityRouteStoresRecordsOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	repository := db.Repositories().Logs
	route := logObservabilityRoute(repository)

	at := time.Now().Add(-time.Minute)
	report := &metricspb.LogReport{
		ServiceName: "storage-node", InstanceId: "storage-node@storage", NodeId: "storage", BootId: "boot-1",
		Records: []*metricspb.LogRecord{
			{Sequence: 1, TimeUnixNano: at.UnixNano(), Level: "ERROR", Message: "写入失败", Caller: "view/commit.go:120", Fields: map[string]string{"view": "v1"}, MergedCount: 4},
			{Sequence: 2, TimeUnixNano: at.Add(time.Second).UnixNano(), Level: "info", Event: "process_started", Message: "启动"},
		},
	}
	require.NoError(t, route(t.Context(), nil, report))
	require.NoError(t, route(t.Context(), nil, report))
	restarted := &metricspb.LogReport{
		ServiceName: "storage-node", InstanceId: "storage-node@storage", NodeId: "storage", BootId: "boot-2",
		Records: []*metricspb.LogRecord{{Sequence: 1, TimeUnixNano: 1, Level: "error", Message: "重启后的第一条"}},
	}
	require.NoError(t, route(t.Context(), nil, restarted))

	rows, err := repository.Query(t.Context(), store.LogRecordFilter{})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	// 时间戳离谱（1970 年）的记录按入库时间保存，不会立刻被清理掉。
	require.Equal(t, "重启后的第一条", rows[0].Message)
	require.WithinDuration(t, time.Now(), rows[0].LoggedAt, time.Minute)
	first := rows[2]
	require.Equal(t, "storage", first.HostID)
	require.Equal(t, "storage-node", first.Component)
	require.Equal(t, "error", first.Level)
	require.Equal(t, "view/commit.go:120", first.Caller)
	require.Equal(t, `{"view":"v1"}`, first.Fields)
	require.Equal(t, 4, first.MergedCount)
	require.WithinDuration(t, at, first.LoggedAt, time.Millisecond)
	require.Equal(t, "process_started", rows[1].Event)
}

func TestLogRecordRowKeyCarriesBootID(t *testing.T) {
	now := time.Now()
	record := logsink.Record{Sequence: 7, Time: now, Level: "error"}
	a := logRecordRow(logsink.Identity{InstanceID: "trade@overseas", BootID: "b1"}, record, now)
	b := logRecordRow(logsink.Identity{InstanceID: "trade@overseas", BootID: "b2"}, record, now)
	require.Equal(t, "trade@overseas|b1|7", a.RecordKey)
	require.NotEqual(t, a.RecordKey, b.RecordKey)
	require.Equal(t, "{}", a.Fields)
}

// Monitor 写自己的重要日志不受日志输出包那 5 秒期限的限制：刚启动时数据库连接被占着，期限到了也要等到写进去。
// 这里用一个已经过期的期限代替“等了超过 5 秒”。
func TestOwnLogDeliverOutlivesTheSinkDeadline(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	repository := db.Repositories().Logs

	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	identity := logsink.Identity{ServiceName: "monitor", InstanceID: "monitor@control", NodeID: "control", BootID: "boot-1"}
	batch := []logsink.Record{{Sequence: 1, Time: time.Now(), Level: "info", Event: "process_started", Message: "monitor 启动"}}
	require.NoError(t, ownLogDeliver(repository)(expired, identity, batch))

	rows, err := repository.Query(t.Context(), store.LogRecordFilter{Component: "monitor"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "process_started", rows[0].Event)
}
