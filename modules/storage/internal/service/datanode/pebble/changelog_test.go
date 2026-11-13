package pebble

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func openChangeLogStore(t *testing.T, retention time.Duration) *Store {
	t.Helper()
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), ChangeLogRetention: retention})
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

	changes := readAllChanges(t, store, goldenSpace, goldenDataset)
	require.Len(t, changes, 3, "一次行写入 + 两条标记；重报不记")
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

// stageTestChange 直接追加一条变更条目，用来构造任意大小的完成标记条目。
func stageTestChange(t *testing.T, s *Store, change *pb.DatasetChange) {
	t.Helper()
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	batch := s.db.NewBatch()
	defer batch.Close()
	require.NoError(t, s.stageChangeLocked(batch, change))
	require.NoError(t, batch.Commit(cpebble.Sync))
	s.noteChangeCommitted()
}

// 大的完成标记条目按字节分页：一页放进响应的条目不超过 maxChangePageBytes（单条超过时单独成页），
// 游标停在最后一条放进响应的条目上，逐页读完不漏不重；别的数据集的条目不占本页字节。
func TestChangeLogPagingLimitsBytes(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	marker := func(datasetID string, size int) *pb.DatasetChange {
		return &pb.DatasetChange{SpaceId: "s", DatasetId: datasetID, Marker: make([]byte, size)}
	}
	part := maxChangePageBytes * 3 / 8
	stageTestChange(t, s, marker("d", part))                 // 1
	stageTestChange(t, s, marker("other", part))             // 2：别的数据集，不计字节
	stageTestChange(t, s, marker("d", part))                 // 3
	stageTestChange(t, s, marker("d", part))                 // 4：放进第一页会超过上限
	stageTestChange(t, s, marker("d", maxChangePageBytes+1)) // 5：单条就超过上限，单独成页
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base, 1)})) // 6

	cursor := &pb.DatasetChangeCursor{StoreId: mustHead(t, s).GetStoreId()}
	var pages [][]uint64
	var cursors []uint64
	for {
		rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: cursor, Limit: maxChangeLimit})
		require.NoError(t, err)
		require.False(t, rsp.GetGap())
		var page []uint64
		for _, change := range rsp.GetChanges() {
			page = append(page, change.GetSequence())
		}
		pages = append(pages, page)
		cursor = rsp.GetNextCursor()
		cursors = append(cursors, cursor.GetSequence())
		if !rsp.GetHasMore() {
			break
		}
	}
	require.Equal(t, [][]uint64{{1, 3}, {4}, {5}, {6}}, pages)
	require.Equal(t, []uint64{3, 4, 5, 6}, cursors)
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

// peekChange 读出的定位字段与完整解码一致，三种条目都能跳过其余字段。
func TestPeekChangeMatchesFullDecode(t *testing.T) {
	for _, change := range []*pb.DatasetChange{
		{Sequence: 7, SpaceId: "s", DatasetId: "d", CreatedAt: "2026-10-01T00:00:00.5Z", Ranges: []*pb.DatasetChangeRange{{SubjectId: "BTC", Freq: "1m", StartTime: "a", EndTime: "b"}}},
		{Sequence: 8, SpaceId: "s", DatasetId: "notes", CreatedAt: "x", Records: []*pb.RecordRowKey{{RecordId: "r", Version: "1"}}},
		{Sequence: 9, SpaceId: "s", DatasetId: "d", CreatedAt: "y", Marker: []byte("marker")},
	} {
		raw, err := proto.Marshal(change)
		require.NoError(t, err)
		got, err := peekChange(raw)
		require.NoError(t, err)
		require.Equal(t, changeHeader{sequence: change.GetSequence(), spaceID: change.GetSpaceId(), datasetID: change.GetDatasetId(), createdAt: change.GetCreatedAt()}, got)
	}
	_, err := peekChange([]byte{0x0a, 0x05, 'x'})
	require.Error(t, err)
}

// 读取与清理并发：每次读要么报断档，要么返回从 cursor+1 起连续的条目，不会静默越过已清理的条目。
func TestReadDatasetChangesConcurrentWithCleanup(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	storeID := mustHead(t, s).GetStoreId()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; i < 200; i++ {
			if err := upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(time.Duration(i)*time.Minute), float64(i))}); err != nil {
				t.Error(err)
				return
			}
			if i%3 == 0 {
				// 截止时间放在未来：每次都清空已有的全部条目。
				if _, err := s.CleanupChangeLog(ctx, time.Now().Add(time.Hour)); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	reads, delivered := 0, 0
	cursor := &pb.DatasetChangeCursor{StoreId: storeID}
	for running := true; running; {
		select {
		case <-writerDone:
			running = false
		default:
		}
		rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: cursor, Limit: 1})
		require.NoError(t, err)
		reads++
		if rsp.GetGap() {
			cursor = mustHead(t, s)
			continue
		}
		want := cursor.GetSequence() + 1
		for _, change := range rsp.GetChanges() {
			require.Equal(t, want, change.GetSequence(), "没有报断档时条目必须从游标之后连续")
			want++
			delivered++
		}
		cursor = rsp.GetNextCursor()
	}
	require.Positive(t, reads)
	require.Positive(t, delivered)
}

// 行写入、能触发账本完成的周期提交、写入方标记三者并发：序号恰好 1..N；
// 每条标记条目与它的 Outbox 条目字节相同；每期账本标记的序号大于该期最后一次行写入的序号。
func TestChangeLogConcurrentWritersKeepSequenceAndOrder(t *testing.T) {
	ctx := context.Background()
	clock := newGoldenClock(goldenPeriod.Add(61 * time.Second))
	store := newGoldenStore(t, clock)
	series := goldenSeries(t, goldenDefaultSeries())
	const periods = 4
	datasets := []string{"plain"}
	exps := make([]DatasetPeriodExpectation, periods)
	for i := range exps {
		dataset := fmt.Sprintf("bars-%d", i)
		datasets = append(datasets, dataset)
		exps[i] = goldenRegister(t, store, clock, goldenSpace, dataset, goldenFreq, series, goldenPeriod, goldenLateness)
	}

	var wg sync.WaitGroup
	for i := range exps {
		wg.Add(1)
		go func(exp DatasetPeriodExpectation) {
			defer wg.Done()
			for index := range series {
				_, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: uint32(index), Row: goldenRow(goldenSpace, exp.DatasetID, goldenFreq, series[index], goldenPeriod, 1)}})
				assert.NoError(t, err)
			}
		}(exps[i])
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			row := goldenRow(goldenSpace, "plain", goldenFreq, series[0], goldenPeriod.Add(-time.Duration(i+1)*time.Minute), float64(i))
			_, err := store.UpsertFields(ctx, goldenSpace, "plain", "", []*pb.RowFieldUpsert{row})
			assert.NoError(t, err)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			marker := goldenProducerMarker(fmt.Sprintf("cause-%d", i), goldenPeriod.Unix(), "complete")
			marker.DatasetId = "plain"
			raw, _, err := BuildProducerPeriodCompletedMessage(goldenSpace, marker)
			if assert.NoError(t, err) {
				_, err = store.AppendDatasetMarker(ctx, raw)
				assert.NoError(t, err)
			}
		}
	}()
	wg.Wait()
	require.Equal(t, "complete", goldenPeriodStatus(t, store, goldenSpace, "bars-0", goldenFreq, goldenPeriod.Unix()))

	outbox := map[string][]byte{}
	entries, err := store.ListOutbox(ctx, 0, 10000)
	require.NoError(t, err)
	for _, entry := range entries {
		message := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(entry.Data, message))
		outbox[message.GetEventId()] = entry.Data
	}
	var all []*pb.DatasetChange
	for _, dataset := range datasets {
		changes := readAllChanges(t, store, goldenSpace, dataset)
		all = append(all, changes...)
		lastRow, markers := uint64(0), 0
		for _, change := range changes {
			if len(change.GetMarker()) == 0 {
				lastRow = max(lastRow, change.GetSequence())
				continue
			}
			markers++
			message, payload := markerPayload(t, change)
			require.Equal(t, outbox[message.GetEventId()], change.GetMarker(), "标记条目与 Outbox 条目字节相同")
			if payload.GetSource() == markerSourceLedger {
				require.Greater(t, change.GetSequence(), lastRow, "账本标记排在该期最后一次行写入之后")
			}
		}
		if dataset == "plain" {
			require.Equal(t, 5, markers)
		} else {
			require.Equal(t, 1, markers)
		}
	}
	// 2 次周期提交 × 4 + 4 条账本标记 + 10 次行写入 + 5 条写入方标记。
	require.Len(t, all, 2*periods+periods+10+5)
	sort.Slice(all, func(i, j int) bool { return all[i].GetSequence() < all[j].GetSequence() })
	for i, change := range all {
		require.Equal(t, uint64(i+1), change.GetSequence())
	}
}

// 等待中的读取：ctx 取消时返回错误；Store 关闭时返回已扫描的位置；一次提交同时唤醒多个等待者；
// ctx 的截止时间早于 wait_ms 时，到截止前返回已扫描的位置而不是报错。
func TestReadDatasetChangesWaitEndings(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s := openChangeLogStore(t, 0)
	head := mustHead(t, s)
	req := func() *pb.ReadDatasetChangesReq {
		return &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: head, WaitMs: 10000}
	}

	t.Run("ctx 取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := s.ReadDatasetChanges(ctx, req())
			done <- err
		}()
		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(2 * time.Second):
			t.Fatal("ctx 取消后等待没有结束")
		}
	})

	t.Run("截止时间早于 wait_ms", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		started := time.Now()
		rsp, err := s.ReadDatasetChanges(ctx, req())
		require.NoError(t, err)
		require.Less(t, time.Since(started), 500*time.Millisecond)
		require.Empty(t, rsp.GetChanges())
		require.Equal(t, head.GetSequence(), rsp.GetNextCursor().GetSequence())
	})

	t.Run("多个等待者同时被唤醒", func(t *testing.T) {
		const waiters = 5
		done := make(chan *pb.ReadDatasetChangesRsp, waiters)
		for i := 0; i < waiters; i++ {
			go func() {
				rsp, err := s.ReadDatasetChanges(context.Background(), req())
				assert.NoError(t, err)
				done <- rsp
			}()
		}
		time.Sleep(50 * time.Millisecond)
		require.NoError(t, upsertTestRows(s, context.Background(), []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base, 1)}))
		for i := 0; i < waiters; i++ {
			select {
			case rsp := <-done:
				require.Len(t, rsp.GetChanges(), 1)
			case <-time.After(2 * time.Second):
				t.Fatal("有等待者没有被唤醒")
			}
		}
	})

	t.Run("Store 关闭", func(t *testing.T) {
		closing, err := Open(Options{Path: filepath.Join(t.TempDir(), "db")})
		require.NoError(t, err)
		closingHead := mustHead(t, closing)
		done := make(chan error, 1)
		go func() {
			_, err := closing.ReadDatasetChanges(context.Background(), &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: closingHead, WaitMs: 10000})
			done <- err
		}()
		time.Sleep(50 * time.Millisecond)
		require.NoError(t, closing.Close())
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("Store 关闭后等待没有结束")
		}
	})
}

// 日志序号连续：游标之后的条目缺了（不论清理位置有没有记下），读取报断档，不会静默越过。
func TestChangeLogMissingEntriesReportGap(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(time.Duration(i)*time.Minute), float64(i))}))
	}
	storeID := mustHead(t, s).GetStoreId()
	read := func(seq uint64) *pb.ReadDatasetChangesRsp {
		rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: &pb.DatasetChangeCursor{StoreId: storeID, Sequence: seq}})
		require.NoError(t, err)
		return rsp
	}
	require.NoError(t, s.db.Delete(changeLogKey(2), cpebble.Sync))
	require.True(t, read(1).GetGap(), "游标 1 之后的第一条是 3")
	require.False(t, read(2).GetGap())
	require.NoError(t, s.db.Delete(changeLogKey(3), cpebble.Sync))
	require.True(t, read(2).GetGap(), "游标落后于头部却没有条目")
	rsp := read(3)
	require.False(t, rsp.GetGap())
	require.Equal(t, uint64(3), rsp.GetNextCursor().GetSequence())
}

// 创建时间读不出来的条目按已过期删除，不会挡住之后的清理；每批从已清理到的序号之后开始。
func TestChangeLogCleanupSkipsPastUnparsableCreatedAt(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	write := func(i int) {
		require.NoError(t, upsertTestRows(s, ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(time.Duration(i)*time.Minute), float64(i))}))
	}
	for i := 0; i < 3; i++ {
		write(i)
	}
	raw, closer, err := s.db.Get(changeLogKey(2))
	require.NoError(t, err)
	change := &pb.DatasetChange{}
	require.NoError(t, proto.Unmarshal(raw, change))
	require.NoError(t, closer.Close())
	change.CreatedAt = "not-a-time"
	broken, err := proto.Marshal(change)
	require.NoError(t, err)
	require.NoError(t, s.db.Set(changeLogKey(2), broken, cpebble.Sync))

	removed, err := s.CleanupChangeLog(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 3, removed)
	trimmed, err := readUint64Meta(s.db, metaChangeTrimmed)
	require.NoError(t, err)
	require.Equal(t, uint64(3), trimmed)

	write(3)
	removed, err = s.CleanupChangeLog(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, removed)
}
