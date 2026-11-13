package follow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/client"
)

var testBase = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

type storeReader struct{ store *pebble.Store }

func (r storeReader) ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq, _ ...client.Option) (*pb.ReadDatasetChangesRsp, error) {
	return r.store.ReadDatasetChanges(ctx, req)
}

func openStore(t *testing.T, path string) *pebble.Store {
	t.Helper()
	store, err := pebble.Open(pebble.Options{Path: path, NodeID: "storage-primary"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func writeRow(t *testing.T, store *pebble.Store, datasetID, subject string, at time.Time) {
	t.Helper()
	_, err := store.UpsertFields(context.Background(), "s", datasetID, "", []*pb.RowFieldUpsert{{
		Key: &pb.RowKey{SpaceId: "s", DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: subject, Freq: "5m", DataTime: at.Format(time.RFC3339Nano),
		}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
	}})
	require.NoError(t, err)
}

func writeMarker(t *testing.T, store *pebble.Store, datasetID string, period time.Time) {
	t.Helper()
	raw, _, err := pebble.BuildProducerPeriodCompletedMessage("s", &pb.DatasetPeriodCompletedMarker{
		DatasetId: datasetID, Frequency: "5m", PeriodTime: period.Unix(), Status: "complete",
		UniverseSubjectIds: []string{"A"}, CauseEventId: fmt.Sprintf("cause-%d", period.Unix()), CompletedAt: timestamppb.New(period),
	})
	require.NoError(t, err)
	_, err = store.AppendDatasetMarker(context.Background(), raw)
	require.NoError(t, err)
}

// fakeTarget 记录应用顺序；跟随进度存在内存里，模拟索引元数据。
type fakeTarget struct {
	mu        sync.Mutex
	saved     viewindex.FollowState
	events    []string
	rebuilds  int
	head      func() viewindex.ChangeCursor
	publishOK func() bool
}

func (t *fakeTarget) LoadState(context.Context) (viewindex.FollowState, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.saved, nil
}

func (t *fakeTarget) ApplyRows(_ context.Context, changes []*pb.DatasetChange, cursor viewindex.ChangeCursor) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, change := range changes {
		t.events = append(t.events, fmt.Sprintf("rows:%s", change.GetRanges()[0].GetSubjectId()))
	}
	t.saved.Cursor = cursor
	return true, nil
}

func (t *fakeTarget) PublishMarker(_ context.Context, change *pb.DatasetChange) error {
	if t.publishOK != nil && !t.publishOK() {
		return errors.New("EventBus 不可用")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, fmt.Sprintf("marker:%d", change.GetSequence()))
	return nil
}

func (t *fakeTarget) SaveState(_ context.Context, state viewindex.FollowState) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.saved.Cursor = state.Cursor
	if state.BuiltSequence > 0 {
		t.saved.BuiltSequence = state.BuiltSequence
	}
	return nil
}

func (t *fakeTarget) Rebuild(context.Context) (viewindex.FollowState, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rebuilds++
	head := t.head()
	t.saved = viewindex.FollowState{Cursor: head, BuiltSequence: head.Sequence}
	return t.saved, nil
}

func (t *fakeTarget) snapshot() ([]string, viewindex.FollowState, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.events...), t.saved, t.rebuilds
}

func newManager(t *testing.T, store *pebble.Store, persist time.Duration) *Manager {
	t.Helper()
	m, err := NewManager(context.Background(), Options{
		Reader: storeReader{store}, Auth: func() *pb.AuthInfo { return &pb.AuthInfo{AppId: "storage-view"} },
		Wait: 2 * time.Second, PersistInterval: persist,
	})
	require.NoError(t, err)
	t.Cleanup(m.Close)
	return m
}

func headOf(t *testing.T, store *pebble.Store) viewindex.ChangeCursor {
	t.Helper()
	head, err := store.ChangeCursor()
	require.NoError(t, err)
	return toCursor(head)
}

func waitApplied(t *testing.T, m *Manager, viewID string) viewindex.ChangeCursor {
	t.Helper()
	ok, head, current, err := m.WaitApplied(context.Background(), "s", viewID, 5*time.Second)
	require.NoError(t, err)
	require.True(t, ok, "5 秒内没有跟上头部 %v，当前 %v", head, current)
	return current
}

// 没有进度的索引先重建；之后行与标记按提交顺序应用，建成序号以内的标记不发布；
// 标记发布失败时停在那条标记上，后面的行不往后应用，恢复后照常继续。
func TestFollowerAppliesInOrderAndStopsAtFailedMarker(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "db"))
	writeRow(t, store, "d", "BEFORE", testBase)
	writeMarker(t, store, "d", testBase)
	var publishOK sync.Map
	publishOK.Store("ok", true)
	target := &fakeTarget{head: func() viewindex.ChangeCursor { return headOf(t, store) }, publishOK: func() bool {
		ok, _ := publishOK.Load("ok")
		return ok.(bool)
	}}
	m := newManager(t, store, time.Hour)
	m.Ensure(Key{SpaceID: "s", ViewID: "v", DatasetID: "d"}, target)
	waitApplied(t, m, "v")
	events, state, rebuilds := target.snapshot()
	require.Equal(t, 1, rebuilds, "没有进度的索引先重建")
	require.Empty(t, events, "重建之前的行与标记不再应用")
	require.Equal(t, uint64(2), state.BuiltSequence)

	writeRow(t, store, "other", "X", testBase)
	writeRow(t, store, "d", "A", testBase.Add(5*time.Minute))
	writeMarker(t, store, "d", testBase.Add(5*time.Minute))
	waitApplied(t, m, "v")
	events, _, _ = target.snapshot()
	require.Equal(t, []string{"rows:A", "marker:5"}, events)

	publishOK.Store("ok", false)
	writeMarker(t, store, "d", testBase.Add(10*time.Minute))
	writeRow(t, store, "d", "B", testBase.Add(15*time.Minute))
	ok, _, current, err := m.WaitApplied(context.Background(), "s", "v", time.Second)
	require.NoError(t, err)
	require.False(t, ok, "标记发布失败时不能跟上头部")
	require.Equal(t, uint64(5), current.Sequence, "停在发布失败的标记之前")
	events, _, _ = target.snapshot()
	require.Equal(t, []string{"rows:A", "marker:5"}, events, "发布失败期间不往后应用行")

	publishOK.Store("ok", true)
	waitApplied(t, m, "v")
	events, _, _ = target.snapshot()
	require.Equal(t, []string{"rows:A", "marker:5", "marker:6", "rows:B"}, events)
}

// 空闲数据集：别的数据集持续写入，跟随器的游标照样前进并定时落盘；日志清理（相当于 8 天没有写入）后重启也不断档、不重建。
func TestIdleFollowerSurvivesChangeLogCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	store := openStore(t, path)
	target := &fakeTarget{head: func() viewindex.ChangeCursor { return headOf(t, store) }}
	m := newManager(t, store, 10*time.Millisecond)
	m.Ensure(Key{SpaceID: "s", ViewID: "idle", DatasetID: "idle"}, target)
	waitApplied(t, m, "idle")
	for i := 0; i < 20; i++ {
		writeRow(t, store, "busy", "X", testBase.Add(time.Duration(i)*5*time.Minute))
	}
	head := headOf(t, store)
	// 空闲数据集没有自己的条目：WaitApplied 扫一遍就算跟上，不等跟随器的长轮询返回。
	started := time.Now()
	waitApplied(t, m, "idle")
	require.Less(t, time.Since(started), time.Second)
	require.Eventually(t, func() bool {
		_, state, _ := target.snapshot()
		return state.Cursor == head
	}, 10*time.Second, 20*time.Millisecond, "游标要扫过别的数据集的条目并落盘")

	removed, err := store.CleanupChangeLog(context.Background(), time.Now().Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 20, removed)
	m.Close()

	restarted := newManager(t, store, time.Hour)
	restarted.Ensure(Key{SpaceID: "s", ViewID: "idle", DatasetID: "idle"}, target)
	writeRow(t, store, "busy", "X", testBase.Add(time.Hour))
	waitApplied(t, restarted, "idle")
	_, state, rebuilds := target.snapshot()
	require.Equal(t, 1, rebuilds, "只有第一次没有进度时重建过")
	require.Equal(t, head.StoreID, state.Cursor.StoreID)
}

// 游标早于日志保留起点时断档，跟随器重建索引后从新的头部继续。
func TestFollowerRebuildsOnGap(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "db"))
	writeRow(t, store, "d", "OLD", testBase)
	writeRow(t, store, "d", "OLD", testBase.Add(5*time.Minute))
	_, err := store.CleanupChangeLog(context.Background(), time.Now().Add(time.Second))
	require.NoError(t, err)
	target := &fakeTarget{head: func() viewindex.ChangeCursor { return headOf(t, store) }}
	target.saved = viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: headOf(t, store).StoreID, Sequence: 1}, BuiltSequence: 1}
	m := newManager(t, store, time.Hour)
	m.Ensure(Key{SpaceID: "s", ViewID: "v", DatasetID: "d"}, target)
	writeRow(t, store, "d", "NEW", testBase.Add(10*time.Minute))
	waitApplied(t, m, "v")
	events, _, rebuilds := target.snapshot()
	require.Equal(t, 1, rebuilds)
	require.NotContains(t, events, "rows:OLD")
}

// 一个 View 的完成标记发布一直失败，只卡住它自己：另一个 View 照常发布、照常跟上。
func TestFailingFollowerDoesNotBlockOthers(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "db"))
	stuck := &fakeTarget{head: func() viewindex.ChangeCursor { return headOf(t, store) }, publishOK: func() bool { return false }}
	healthy := &fakeTarget{head: func() viewindex.ChangeCursor { return headOf(t, store) }}
	m := newManager(t, store, time.Hour)
	m.Ensure(Key{SpaceID: "s", ViewID: "stuck", DatasetID: "a"}, stuck)
	m.Ensure(Key{SpaceID: "s", ViewID: "healthy", DatasetID: "b"}, healthy)
	waitApplied(t, m, "stuck")
	waitApplied(t, m, "healthy")

	for i := 0; i < 3; i++ {
		period := testBase.Add(time.Duration(i) * 5 * time.Minute)
		writeRow(t, store, "a", "A", period)
		writeMarker(t, store, "a", period)
		writeRow(t, store, "b", "B", period)
		writeMarker(t, store, "b", period)
	}
	waitApplied(t, m, "healthy")
	events, _, _ := healthy.snapshot()
	require.Equal(t, []string{"rows:B", "marker:4", "rows:B", "marker:8", "rows:B", "marker:12"}, events)
	ok, _, _, err := m.WaitApplied(context.Background(), "s", "stuck", 200*time.Millisecond)
	require.NoError(t, err)
	require.False(t, ok, "发布一直失败的 View 停在第一条标记上")
	events, _, _ = stuck.snapshot()
	require.Equal(t, []string{"rows:A"}, events)
}

// blockingReader 在 block 打开后让按游标的读取一直等到调用方的截止时间，模拟预扫描很慢。
type blockingReader struct {
	inner storeReader
	block *sync.Map
}

func (r blockingReader) ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq, opts ...client.Option) (*pb.ReadDatasetChangesRsp, error) {
	if on, _ := r.block.Load("on"); on == true && !req.GetFromHead() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.inner.ReadDatasetChanges(ctx, req, opts...)
}

// 等待时长用完时预扫描还没扫到头部：回答“还没跟上”，不是错误。
func TestWaitAppliedTimeoutDuringScanIsNotAnError(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "db"))
	block := &sync.Map{}
	m, err := NewManager(context.Background(), Options{
		Reader: blockingReader{inner: storeReader{store}, block: block}, Auth: func() *pb.AuthInfo { return &pb.AuthInfo{AppId: "storage-view"} },
		Wait: 2 * time.Second, PersistInterval: time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(m.Close)
	target := &fakeTarget{head: func() viewindex.ChangeCursor { return headOf(t, store) }}
	m.Ensure(Key{SpaceID: "s", ViewID: "v", DatasetID: "d"}, target)
	waitApplied(t, m, "v")

	block.Store("on", true)
	writeRow(t, store, "d", "A", testBase)
	ok, _, _, err := m.WaitApplied(context.Background(), "s", "v", 200*time.Millisecond)
	require.NoError(t, err)
	require.False(t, ok)
	block.Store("on", false)
}
