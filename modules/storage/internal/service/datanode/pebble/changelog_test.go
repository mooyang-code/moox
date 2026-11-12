package pebble

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func openChangeLogStore(t *testing.T, retention time.Duration) *Store {
	t.Helper()
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1", ChangeLogRetention: retention})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustHead(t *testing.T, s *Store) *pb.DatasetChangeCursor {
	t.Helper()
	head, err := s.ChangeCursor()
	require.NoError(t, err)
	return head
}

// readAllChanges 从序号 0 读出一个数据集的全部变更。
func readAllChanges(t *testing.T, s *Store, spaceID, datasetID string) []*pb.DatasetChange {
	t.Helper()
	cursor := &pb.DatasetChangeCursor{StoreId: mustHead(t, s).GetStoreId()}
	var out []*pb.DatasetChange
	for {
		rsp, err := s.ReadDatasetChanges(context.Background(), &pb.ReadDatasetChangesReq{SpaceId: spaceID, DatasetId: datasetID, Cursor: cursor})
		require.NoError(t, err)
		require.False(t, rsp.GetGap())
		out = append(out, rsp.GetChanges()...)
		cursor = rsp.GetNextCursor()
		if !rsp.GetHasMore() {
			return out
		}
	}
}

func recordRow(spaceID, datasetID, recordID, version string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: recordID, Version: version}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: recordID}}}},
	}
}

// markerPayload 解出完成标记条目内嵌的周期完成事件。
func markerPayload(t *testing.T, change *pb.DatasetChange) (*eventpb.EventMessage, *storageeventpb.DatasetPeriodCompleted) {
	t.Helper()
	require.NotEmpty(t, change.GetMarker())
	require.Empty(t, change.GetRanges())
	require.Empty(t, change.GetRecords())
	message := &eventpb.EventMessage{}
	require.NoError(t, proto.Unmarshal(change.GetMarker(), message))
	payload := &storageeventpb.DatasetPeriodCompleted{}
	require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
	return message, payload
}

func TestChangeLogRecordsWritesInOrder(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	head, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", FromHead: true})
	require.NoError(t, err)
	require.Equal(t, uint64(0), head.GetNextCursor().GetSequence())
	require.Equal(t, s.sourceStoreID, head.GetNextCursor().GetStoreId())
	require.Empty(t, head.GetChanges())

	// 一次写入里同一标的的多行合并成一个时间范围，两个数据集各记一条；时间保留到纳秒。
	require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{
		tsRow("BTCUSDT", "", base, 1), tsRow("BTCUSDT", "", base.Add(2*time.Minute+1500*time.Millisecond), 2), tsRow("ETHUSDT", "", base.Add(time.Minute), 3),
	}))
	other := tsRow("BTCUSDT", "", base, 9)
	other.Key.DatasetId = "other"
	require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{other}))

	got, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: head.GetNextCursor()})
	require.NoError(t, err)
	require.False(t, got.GetGap())
	require.Len(t, got.GetChanges(), 1, "只返回请求的数据集")
	change := got.GetChanges()[0]
	require.Equal(t, uint64(1), change.GetSequence())
	require.Empty(t, change.GetMarker())
	require.Empty(t, change.GetRecords())
	require.Len(t, change.GetRanges(), 2)
	require.Equal(t, "BTCUSDT", change.GetRanges()[0].GetSubjectId())
	require.Equal(t, "2026-10-01T00:00:00Z", change.GetRanges()[0].GetStartTime())
	require.Equal(t, "2026-10-01T00:02:01.5Z", change.GetRanges()[0].GetEndTime())
	require.Equal(t, "ETHUSDT", change.GetRanges()[1].GetSubjectId())
	// 游标按全部变更推进（包括别的数据集的序号 2）。
	require.Equal(t, uint64(2), got.GetNextCursor().GetSequence())

	// 从新游标继续读，没有新变更。
	again, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: got.GetNextCursor()})
	require.NoError(t, err)
	require.Empty(t, again.GetChanges())
	require.False(t, again.GetGap())
	require.Equal(t, uint64(2), again.GetNextCursor().GetSequence())
}

// 记录型行记被写入的记录键：去重、按 (record_id, version) 排序，不带时间范围。
func TestChangeLogRecordsRecordKeys(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	_, err := s.UpsertFields(ctx, "s", "notes", "", []*pb.RowFieldUpsert{
		recordRow("s", "notes", "b", "1"), recordRow("s", "notes", "a", "2"), recordRow("s", "notes", "a", "1"),
	})
	require.NoError(t, err)
	changes := readAllChanges(t, s, "s", "notes")
	require.Len(t, changes, 1)
	require.Empty(t, changes[0].GetRanges())
	require.Empty(t, changes[0].GetMarker())
	var keys [][2]string
	for _, record := range changes[0].GetRecords() {
		keys = append(keys, [2]string{record.GetRecordId(), record.GetVersion()})
	}
	require.Equal(t, [][2]string{{"a", "1"}, {"a", "2"}, {"b", "1"}}, keys)
}

// 同一数据集里行、账本标记、记录型行、写入方标记按提交先后排序：标记条目之前的行都已提交。
func TestChangeLogOrdersRowsMarkersAndRecords(t *testing.T) {
	ctx := context.Background()
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	series := goldenSeries(t, goldenDefaultSeries())
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod, goldenLateness)

	// 先提交一个序列（等待中），再提交另一个序列让账本判定完成：完成标记在第二次行写入之后。
	_, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[0], goldenPeriod, 1)}})
	require.NoError(t, err)
	_, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[1], goldenPeriod, 1)}})
	require.NoError(t, err)
	require.Equal(t, "complete", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix()))
	_, err = store.UpsertFields(ctx, goldenSpace, goldenDataset, "", []*pb.RowFieldUpsert{recordRow(goldenSpace, goldenDataset, "note", "1")})
	require.NoError(t, err)
	marker := goldenProducerMarker("source-ready", goldenPeriod.Unix(), "complete")
	marker.DatasetId = goldenDataset
	raw, _, err := BuildProducerPeriodCompletedMessage(goldenSpace, marker)
	require.NoError(t, err)
	_, err = store.AppendDatasetMarker(ctx, raw)
	require.NoError(t, err)

	changes := readAllChanges(t, store, goldenSpace, goldenDataset)
	require.Len(t, changes, 5)
	kinds := make([]string, len(changes))
	for i, change := range changes {
		switch {
		case len(change.GetMarker()) > 0:
			_, payload := markerPayload(t, change)
			kinds[i] = "marker:" + payload.GetSource()
		case len(change.GetRecords()) > 0:
			kinds[i] = "records"
		default:
			kinds[i] = "ranges:" + change.GetRanges()[0].GetSubjectId()
		}
		if i > 0 {
			require.Greater(t, change.GetSequence(), changes[i-1].GetSequence())
		}
	}
	require.Equal(t, []string{"ranges:" + series[0].SubjectID, "ranges:" + series[1].SubjectID, "marker:ledger", "records", "marker:producer"}, kinds)
}

// 两个标记写入点都进日志，条目内嵌的字节与 Outbox 里那条完全相同；截止判定的降级也进日志。
// 内容相同的重报不多记一条，同步点不进日志。
func TestChangeLogRecordsBothMarkerWriters(t *testing.T) {
	ctx := context.Background()
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	series := goldenSeries(t, goldenDefaultSeries())
	exp := goldenRegister(t, store, clock, goldenSpace, goldenDataset, goldenFreq, series, goldenPeriod, goldenLateness)
	_, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow(goldenSpace, goldenDataset, goldenFreq, series[0], goldenPeriod, 1)}})
	require.NoError(t, err)
	// 截止扫描判定降级（账本写入点）。
	clock.set(goldenPeriod.Add(goldenDeadline))
	require.NoError(t, goldenFinalize(t, store, clock.now()))
	require.Equal(t, "degraded", goldenPeriodStatus(t, store, goldenSpace, goldenDataset, goldenFreq, goldenPeriod.Unix()))

	// 写入方上报（第二个写入点），重报一次。
	marker := goldenProducerMarker("source-ready", goldenPeriod.Unix(), "complete")
	marker.DatasetId = goldenDataset
	raw, _, err := BuildProducerPeriodCompletedMessage(goldenSpace, marker)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = store.AppendDatasetMarker(ctx, raw)
		require.NoError(t, err)
	}
	// 同步点只进 Outbox。
	syncRaw, _, err := BuildDatasetSyncPointMessage(goldenSpace, &pb.DatasetSyncPointMarker{RequestId: "req-1", DatasetId: goldenDataset, Source: "import"})
	require.NoError(t, err)
	_, err = store.AppendDatasetMarker(ctx, syncRaw)
	require.NoError(t, err)

	changes := readAllChanges(t, store, goldenSpace, goldenDataset)
	require.Len(t, changes, 3, "一次行写入 + 两条标记；重报与同步点不记")
	require.NotEmpty(t, changes[0].GetRanges())
	outbox := goldenMarkers(t, store, goldenSpace, goldenDataset)
	require.Len(t, outbox, 2)
	for i, want := range []string{"ledger", "producer"} {
		message, payload := markerPayload(t, changes[i+1])
		require.Equal(t, want, payload.GetSource())
		require.Equal(t, goldenSpace, changes[i+1].GetSpaceId())
		require.Equal(t, goldenDataset, changes[i+1].GetDatasetId())
		wantRaw, err := proto.Marshal(outbox[i].message)
		require.NoError(t, err)
		gotRaw, err := proto.Marshal(message)
		require.NoError(t, err)
		require.Equal(t, wantRaw, gotRaw, "日志里的标记与 Outbox 里的是同一条消息")
	}
	_, payload := markerPayload(t, changes[1])
	require.Equal(t, "degraded", payload.GetStatus())
	_, payload = markerPayload(t, changes[2])
	require.Equal(t, "factor.detail", payload.GetDetailType())
	require.Equal(t, []byte("detail-source-ready"), payload.GetDetail())
}

func TestChangeLogPagingAndHasMore(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(time.Duration(i)*time.Minute), float64(i))}))
	}
	cursor := &pb.DatasetChangeCursor{StoreId: mustHead(t, s).GetStoreId()}
	var seen []uint64
	for {
		rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: cursor, Limit: 2})
		require.NoError(t, err)
		for _, c := range rsp.GetChanges() {
			seen = append(seen, c.GetSequence())
		}
		cursor = rsp.GetNextCursor()
		if !rsp.GetHasMore() {
			break
		}
	}
	require.Equal(t, []uint64{1, 2, 3, 4, 5}, seen)
}

// 等待中的读取在下一次提交后立即返回：行写入和完成标记都能唤醒；别的数据集的写入只推进游标、继续等。
func TestReadDatasetChangesWaitReturnsAfterCommit(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cursor := mustHead(t, s)

	type result struct {
		rsp     *pb.ReadDatasetChangesRsp
		err     error
		elapsed time.Duration
	}
	wait := func(cursor *pb.DatasetChangeCursor) <-chan result {
		done := make(chan result, 1)
		go func() {
			started := time.Now()
			rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: cursor, WaitMs: 10000})
			done <- result{rsp: rsp, err: err, elapsed: time.Since(started)}
		}()
		return done
	}

	// 行写入唤醒；中途别的数据集的写入不让它返回。
	done := wait(cursor)
	time.Sleep(50 * time.Millisecond)
	other := tsRow("BTCUSDT", "", base, 1)
	other.Key.DatasetId = "other"
	require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{other}))
	time.Sleep(50 * time.Millisecond)
	select {
	case got := <-done:
		t.Fatalf("别的数据集的写入不应让读取返回：%+v", got.rsp)
	default:
	}
	require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base, 2)}))
	got := <-done
	require.NoError(t, got.err)
	require.Less(t, got.elapsed, 5*time.Second)
	require.Len(t, got.rsp.GetChanges(), 1)
	require.Equal(t, uint64(2), got.rsp.GetChanges()[0].GetSequence())
	require.Equal(t, uint64(2), got.rsp.GetNextCursor().GetSequence())

	// 完成标记唤醒。
	done = wait(got.rsp.GetNextCursor())
	time.Sleep(50 * time.Millisecond)
	marker := goldenProducerMarker("source-ready", goldenPeriod.Unix(), "complete")
	marker.DatasetId = "d"
	raw, _, err := BuildProducerPeriodCompletedMessage("s", marker)
	require.NoError(t, err)
	_, err = s.AppendDatasetMarker(ctx, raw)
	require.NoError(t, err)
	got = <-done
	require.NoError(t, got.err)
	require.Less(t, got.elapsed, 5*time.Second)
	require.Len(t, got.rsp.GetChanges(), 1)
	markerPayload(t, got.rsp.GetChanges()[0])

	// 到期仍没有本数据集的变更：返回空，游标前进到已扫描的位置（别的数据集的序号）。
	require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{other}))
	started := time.Now()
	rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: got.rsp.GetNextCursor(), WaitMs: 100})
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(started), 100*time.Millisecond)
	require.Empty(t, rsp.GetChanges())
	require.Equal(t, mustHead(t, s).GetSequence(), rsp.GetNextCursor().GetSequence())
}

// 断档：游标早于清理位置、比头部还新、store_id 对不上。断档即使带 wait_ms 也立即返回。
func TestChangeLogGapDetection(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, time.Hour)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(time.Duration(i)*time.Minute), float64(i))}))
	}
	storeID := mustHead(t, s).GetStoreId()

	// 把 cutoff 放到未来，全部过期后再写一条新的。
	removed, err := s.CleanupChangeLog(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 4, removed)
	require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(10*time.Minute), 10)}))

	gapCases := map[string]*pb.DatasetChangeCursor{
		"早于清理到的序号":     {StoreId: storeID, Sequence: 0},
		"比头部还新":        {StoreId: storeID, Sequence: 99},
		"store_id 对不上": {StoreId: "rebuilt", Sequence: 5},
	}
	for name, cursor := range gapCases {
		started := time.Now()
		rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: cursor, WaitMs: 10000})
		require.NoError(t, err, name)
		require.True(t, rsp.GetGap(), name)
		require.Less(t, time.Since(started), time.Second, name)
	}

	// 游标正好在清理边界上：没有断档，读到之后的新变更。
	rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: &pb.DatasetChangeCursor{StoreId: storeID, Sequence: 4}})
	require.NoError(t, err)
	require.False(t, rsp.GetGap())
	require.Len(t, rsp.GetChanges(), 1)
	require.Equal(t, uint64(5), rsp.GetChanges()[0].GetSequence())
}

func TestChangeLogCleanupKeepsRecentEntries(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 7*24*time.Hour)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base, 1)}))
	removed, err := s.CleanupChangeLog(ctx, time.Now().Add(-s.ChangeLogRetention()))
	require.NoError(t, err)
	require.Equal(t, 0, removed)
	require.Len(t, readAllChanges(t, s, "s", "d"), 1)
}

// 带 commit_id 的写入与其他时序写入一样记变更日志；同一 commit_id 的重复提交不再写入，也不多记一条。
func TestCommitWritesAreChangeLoggedOnce(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	row := tsRow("BTCUSDT", "", base, 1)
	for i := 0; i < 2; i++ {
		_, err := s.UpsertFields(ctx, "s", "d", "commit-1", []*pb.RowFieldUpsert{row})
		require.NoError(t, err)
	}
	require.Equal(t, uint64(1), mustHead(t, s).GetSequence())
}
