package rpc

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestQueryLogRecordsPagesAndValidates(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	repos := db.Repositories()
	base := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	_, err = repos.Logs.InsertBatch(t.Context(), []domain.LogRecord{
		{RecordKey: "k1", HostID: "storage", Component: "storage-node", Level: "error", Message: "第一条", Caller: "a.go:1", Fields: `{"view":"v1"}`, MergedCount: 2, LoggedAt: base},
		{RecordKey: "k2", HostID: "storage", Component: "storage-node", Level: "info", Event: "process_started", Message: "第二条", Fields: "{}", LoggedAt: base.Add(time.Minute)},
		{RecordKey: "k3", HostID: "control", Component: "monitor", Level: "error", Message: "第三条", Fields: "{}", LoggedAt: base.Add(2 * time.Minute)},
	})
	require.NoError(t, err)
	service := New(repos, Options{})

	first, err := service.QueryLogRecords(t.Context(), &monitorpb.QueryLogRecordsReq{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, first.GetRetInfo().GetCode())
	require.True(t, first.GetHasMore())
	require.Len(t, first.GetRecords(), 2)
	require.Equal(t, "第三条", first.GetRecords()[0].GetMessage())
	require.Equal(t, "2026-11-12T04:02:00Z", first.GetRecords()[0].GetLoggedAt())

	next, err := service.QueryLogRecords(t.Context(), &monitorpb.QueryLogRecordsReq{Limit: 2, BeforeId: first.GetRecords()[1].GetId()})
	require.NoError(t, err)
	require.False(t, next.GetHasMore())
	require.Len(t, next.GetRecords(), 1)
	record := next.GetRecords()[0]
	require.Equal(t, "第一条", record.GetMessage())
	require.Equal(t, "storage", record.GetHostId())
	require.Equal(t, "storage-node", record.GetComponent())
	require.Equal(t, "v1", record.GetFields()["view"])
	require.Equal(t, int32(2), record.GetMergedCount())

	filtered, err := service.QueryLogRecords(t.Context(), &monitorpb.QueryLogRecordsReq{Component: "storage-node", Level: "ERROR", StartAt: "2026-11-12T03:00:00Z", EndAt: "2026-11-12T05:00:00+00:00"})
	require.NoError(t, err)
	require.Len(t, filtered.GetRecords(), 1)

	for _, req := range []*monitorpb.QueryLogRecordsReq{{Level: "verbose"}, {StartAt: "昨天"}, {EndAt: "2026-11-12"}} {
		rsp, err := service.QueryLogRecords(t.Context(), req)
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "%+v", req)
	}
	unavailable, err := (&Service{}).QueryLogRecords(t.Context(), &monitorpb.QueryLogRecordsReq{})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, unavailable.GetRetInfo().GetCode())
}

// 一页取满 200 条时也要能看出后面还有。
func TestQueryLogRecordsReportsMoreAtTheMaximumPageSize(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	repos := db.Repositories()
	base := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	batch := make([]domain.LogRecord, 0, store.MaxLogRecordPage+1)
	for i := 0; i <= store.MaxLogRecordPage; i++ {
		batch = append(batch, domain.LogRecord{RecordKey: fmt.Sprintf("k%d", i), HostID: "control", Component: "monitor", Level: "error", Message: "x", Fields: "{}", LoggedAt: base.Add(time.Duration(i) * time.Second)})
	}
	_, err = repos.Logs.InsertBatch(t.Context(), batch)
	require.NoError(t, err)
	rsp, err := New(repos, Options{}).QueryLogRecords(t.Context(), &monitorpb.QueryLogRecordsReq{Limit: 500})
	require.NoError(t, err)
	require.Len(t, rsp.GetRecords(), store.MaxLogRecordPage)
	require.True(t, rsp.GetHasMore())
}
