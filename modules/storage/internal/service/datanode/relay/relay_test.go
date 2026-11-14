package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
)

type testPublisher struct {
	values   [][]byte
	attempts [][]byte
	calls    int
	failAt   int
}

func (p *testPublisher) PublishMessage(_ context.Context, data []byte) error {
	p.calls++
	p.attempts = append(p.attempts, append([]byte(nil), data...))
	if p.failAt > 0 && p.calls == p.failAt {
		return errors.New("publish failed")
	}
	p.values = append(p.values, append([]byte(nil), data...))
	return nil
}

type blockingPublisher struct{}

func (blockingPublisher) PublishMessage(ctx context.Context, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

type duplicatePublisher struct{ *testPublisher }

func (p *duplicatePublisher) PublishMessageWithAck(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
	if err := p.PublishMessage(ctx, data); err != nil {
		return nil, err
	}
	return &jetstream.PublishAck{Duplicate: true}, nil
}

type reconnectPublisher struct {
	values       [][]byte
	unavailable  bool
	reconnects   int
	reconnectErr error
}

func (p *reconnectPublisher) PublishMessage(_ context.Context, data []byte) error {
	if p.unavailable {
		return errors.New("eventbus connection is stale")
	}
	p.values = append(p.values, append([]byte(nil), data...))
	return nil
}

func (p *reconnectPublisher) Reconnect(context.Context) error {
	p.reconnects++
	if p.reconnectErr != nil {
		return p.reconnectErr
	}
	p.unavailable = false
	return nil
}

func (p *reconnectPublisher) Ready() bool { return !p.unavailable }

type stopAfterFirstPublisher struct {
	relay    *Relay
	attempts int
}

func (p *stopAfterFirstPublisher) PublishMessage(context.Context, []byte) error {
	p.attempts++
	p.relay.Close()
	return nil
}

type countingPublisher struct{ published atomic.Int32 }

func (p *countingPublisher) PublishMessage(context.Context, []byte) error {
	p.published.Add(1)
	return nil
}

func openTestStore(t *testing.T) *pebble.Store {
	t.Helper()
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustAppendMarker(t *testing.T, store *pebble.Store, spaceID, datasetID string) string {
	t.Helper()
	id, err := appendTestMarker(context.Background(), store, spaceID, datasetID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func upsertTestRow(t *testing.T, store *pebble.Store, datasetID string, minute int) {
	t.Helper()
	key := &pb.RowKey{SpaceId: "s", DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: fmt.Sprintf("2026-08-16T00:%02d:00Z", minute),
	}}}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	if err := store.UpsertFields(context.Background(), "s", datasetID, []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
}

func savedCursor(t *testing.T, store *pebble.Store) uint64 {
	t.Helper()
	cursor, err := store.MarkerRelayCursor()
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

func eventIDs(t *testing.T, values [][]byte) []string {
	t.Helper()
	out := make([]string, 0, len(values))
	for _, raw := range values {
		message := &eventpb.EventMessage{}
		if err := proto.Unmarshal(raw, message); err != nil {
			t.Fatal(err)
		}
		out = append(out, message.GetEventId())
	}
	return out
}

func newTestMetrics(t *testing.T) *observability.ViewMetrics {
	t.Helper()
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return metrics
}

// 按变更日志的顺序发布；某一条失败就停在它上面，游标只推进到它前面，重试发的是同一条消息（事件 ID 不变）。
func TestRelayStopsAtFailedMarkerAndRetriesIt(t *testing.T) {
	store := openTestStore(t)
	first := mustAppendMarker(t, store, "s", "d")
	second := mustAppendMarker(t, store, "s", "d")
	publisher := &testPublisher{failAt: 2}
	metrics := newTestMetrics(t)
	relay, err := NewRelay(store, publisher, RelayOptions{BatchSize: 10, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	failed := metrics.Snapshot()
	if failed.OutboxPublishErrorsTotal != 1 || !failed.OutboxObserved || failed.OutboxPendingEntries != 1 || failed.OutboxOldestAge <= 0 {
		t.Fatalf("发布失败后的积压状态 = %+v，期望 1 条待发", failed)
	}
	if got := savedCursor(t, store); got != 1 {
		t.Fatalf("失败后落盘的游标 = %d，期望停在已发布的第 1 条", got)
	}
	publisher.failAt = 0
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 3 || !bytes.Equal(publisher.attempts[1], publisher.attempts[2]) {
		t.Fatalf("重试次数 = %d，重试内容应与失败的那次相同", len(publisher.attempts))
	}
	if got := eventIDs(t, publisher.values); len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("发布顺序 = %v，期望 [%s %s]", got, first, second)
	}
	if got := savedCursor(t, store); got != 2 {
		t.Fatalf("全部发布后落盘的游标 = %d，期望 2", got)
	}
	if after := metrics.Snapshot(); after.OutboxPendingEntries != 0 || after.OutboxOldestAge != 0 {
		t.Fatalf("全部发布后的积压状态 = %+v", after)
	}
	// 再扫一轮不重发。
	if err := relay.flush(context.Background()); err != nil || len(publisher.values) != 2 {
		t.Fatalf("重复发布：values=%d err=%v", len(publisher.values), err)
	}
}

// 重启（新建转发协程）后从落盘的游标接着发，已发布的不再发。
func TestRelayResumesFromSavedCursorAfterRestart(t *testing.T) {
	store := openTestStore(t)
	mustAppendMarker(t, store, "s", "d")
	mustAppendMarker(t, store, "s", "d")
	before := &testPublisher{}
	relay, err := NewRelay(store, before, RelayOptions{Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil || len(before.values) != 2 {
		t.Fatalf("values=%d err=%v", len(before.values), err)
	}
	third := mustAppendMarker(t, store, "s", "d")

	after := &testPublisher{}
	restarted, err := NewRelay(store, after, RelayOptions{Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := eventIDs(t, after.values); len(got) != 1 || got[0] != third {
		t.Fatalf("重启后发布 = %v，期望只有 %s", got, third)
	}
}

// 发布成功但游标还没落盘就退出：重启后重发同一条消息，事件 ID 不变，下游按事件 ID 去重。
func TestRelayRepublishesSameEventWhenCursorWasNotSaved(t *testing.T) {
	store := openTestStore(t)
	id := mustAppendMarker(t, store, "s", "d")
	first := &testPublisher{}
	relay, err := NewRelay(store, first, RelayOptions{Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 模拟落盘丢失：把游标改回 0。
	if err := store.SetMarkerRelayCursor(0); err != nil {
		t.Fatal(err)
	}
	second := &testPublisher{}
	restarted, err := NewRelay(store, second, RelayOptions{Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(second.values) != 1 || !bytes.Equal(first.values[0], second.values[0]) || eventIDs(t, second.values)[0] != id {
		t.Fatalf("重发的消息应与第一次完全相同")
	}
}

// 游标落在变更日志保留期之外：跳到最早可用的条目接着发，被清理掉的不发。
func TestRelaySkipsToEarliestEntryWhenCursorFellBehindRetention(t *testing.T) {
	store := openTestStore(t)
	mustAppendMarker(t, store, "s", "d")
	mustAppendMarker(t, store, "s", "d")
	time.Sleep(20 * time.Millisecond)
	cutoff := time.Now().UTC()
	time.Sleep(20 * time.Millisecond)
	kept := mustAppendMarker(t, store, "s", "d")
	if deleted, err := store.CleanupChangeLog(context.Background(), cutoff); err != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	publisher := &testPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := eventIDs(t, publisher.values); len(got) != 1 || got[0] != kept {
		t.Fatalf("发布 = %v，期望只有保留期内的 %s", got, kept)
	}
	if got := savedCursor(t, store); got != 3 {
		t.Fatalf("游标 = %d，期望 3", got)
	}
}

// 数据集删除后，它还没转发的完成标记不再发布，其他数据集的照常发。
func TestRelaySkipsMarkersOfDeletedDataset(t *testing.T) {
	store := openTestStore(t)
	mustAppendMarker(t, store, "s", "deleted")
	kept := mustAppendMarker(t, store, "s", "kept")
	if _, err := store.DeleteDatasetRows(context.Background(), "s", "deleted"); err != nil {
		t.Fatal(err)
	}
	publisher := &testPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := eventIDs(t, publisher.values); len(got) != 1 || got[0] != kept {
		t.Fatalf("发布 = %v，期望只有 %s", got, kept)
	}
}

// 只有行写入时游标在内存里跟着日志头部走，但不是每轮都落盘；发布过完成标记的那一轮立即落盘。
func TestRelayAdvancesPastRowEntriesAndSavesCursorAfterPublish(t *testing.T) {
	store := openTestStore(t)
	for minute := 0; minute < 3; minute++ {
		upsertTestRow(t, store, "d", minute)
	}
	publisher := &testPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if relay.cursor != 3 || savedCursor(t, store) != 0 || len(publisher.values) != 0 {
		t.Fatalf("内存游标=%d 落盘游标=%d 发布=%d，期望 3、0、0", relay.cursor, savedCursor(t, store), len(publisher.values))
	}
	mustAppendMarker(t, store, "s", "d")
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if savedCursor(t, store) != 4 || len(publisher.values) != 1 {
		t.Fatalf("落盘游标=%d 发布=%d，期望 4、1", savedCursor(t, store), len(publisher.values))
	}
	// 只扫描的进度过了落盘间隔也会落盘，游标不会一直停在保留期之外。
	upsertTestRow(t, store, "d", 10)
	relay.savedAt = time.Now().Add(-2 * cursorSaveInterval)
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := savedCursor(t, store); got != 5 {
		t.Fatalf("落盘游标=%d，期望 5", got)
	}
}

func TestRelayBoundsPublishAttempt(t *testing.T) {
	store := openTestStore(t)
	mustAppendMarker(t, store, "s", "d")
	relay, err := NewRelay(store, blockingPublisher{}, RelayOptions{PublishTimeout: 10 * time.Millisecond, Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := relay.flush(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("flush error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("publish attempt took %s, want bounded", elapsed)
	}
	if got := savedCursor(t, store); got != 0 {
		t.Fatalf("超时后游标 = %d，期望不推进", got)
	}
}

func TestRelayReconnectsPublisherAfterRepeatedFailures(t *testing.T) {
	store := openTestStore(t)
	mustAppendMarker(t, store, "s", "d")
	publisher := &reconnectPublisher{unavailable: true}
	metrics := newTestMetrics(t)
	relay, err := NewRelay(store, publisher, RelayOptions{ReconnectAfterFailures: 3, ReconnectCooldown: time.Nanosecond, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := relay.flush(context.Background()); err == nil {
			t.Fatal("expected stale publisher failure")
		}
	}
	if publisher.reconnects != 1 {
		t.Fatalf("reconnects=%d, want 1", publisher.reconnects)
	}
	afterReconnect := metrics.Snapshot()
	if !afterReconnect.OutboxPublisherReady || afterReconnect.OutboxReconnectSuccesses != 1 || afterReconnect.OutboxReconnectStatus != "success" {
		t.Fatalf("publisher state after reconnect = %+v", afterReconnect)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if metrics.Snapshot().OutboxLastPublishSuccess.IsZero() {
		t.Fatal("successful publish timestamp was not recorded")
	}
	if len(publisher.values) != 1 || savedCursor(t, store) != 1 {
		t.Fatalf("重连后 values=%d cursor=%d", len(publisher.values), savedCursor(t, store))
	}
}

func TestRelayKeepsPublisherUnreadyWhenReconnectVerificationFails(t *testing.T) {
	store := openTestStore(t)
	mustAppendMarker(t, store, "s", "d")
	metrics := newTestMetrics(t)
	publisher := &reconnectPublisher{unavailable: true, reconnectErr: errors.New("replacement unavailable")}
	relay, err := NewRelay(store, publisher, RelayOptions{ReconnectAfterFailures: 1, ReconnectCooldown: time.Nanosecond, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err == nil {
		t.Fatal("expected publish failure")
	}
	snapshot := metrics.Snapshot()
	if snapshot.OutboxPublisherReady || snapshot.OutboxReconnectFailures != 1 || snapshot.OutboxReconnectStatus != "failed" {
		t.Fatalf("publisher state after failed reconnect = %+v", snapshot)
	}
	if !snapshot.OutboxObserved || snapshot.OutboxPendingEntries != 1 {
		t.Fatalf("积压状态 = %+v，期望 1 条待发", snapshot)
	}
}

func TestRelayRecordsDuplicateAcknowledgement(t *testing.T) {
	store := openTestStore(t)
	mustAppendMarker(t, store, "s", "d")
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewRelay(store, &duplicatePublisher{testPublisher: &testPublisher{}}, RelayOptions{Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	expected := "# HELP moox_storage_outbox_duplicate_publish_total Storage outbox publishes acknowledged as duplicates.\n# TYPE moox_storage_outbox_duplicate_publish_total counter\nmoox_storage_outbox_duplicate_publish_total 1\n"
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), "moox_storage_outbox_duplicate_publish_total"); err != nil {
		t.Fatal(err)
	}
}

// 关闭后不再发下一条；已发布的那条的进度落盘，其余留给下次启动。
func TestRelayStopsStartingNewPublishesAfterClose(t *testing.T) {
	store := openTestStore(t)
	for i := 0; i < 3; i++ {
		mustAppendMarker(t, store, "s", "d")
	}
	publisher := &stopAfterFirstPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{BatchSize: 10, Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	publisher.relay = relay
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if publisher.attempts != 1 {
		t.Fatalf("关闭后仍继续发布：attempts = %d", publisher.attempts)
	}
	if got := savedCursor(t, store); got != 1 {
		t.Fatalf("游标 = %d，期望停在已发布的第 1 条，其余 2 条留待下次", got)
	}
}

func waitPublished(t *testing.T, publisher *countingPublisher, want int32, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for publisher.published.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%v 内只发布了 %d 条，期望 %d 条", within, publisher.published.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 完成标记提交后立即发布，不等定时扫描（间隔设成 1 小时）。
func TestRelayPublishesRightAfterCommitWithoutWaitingForThePoll(t *testing.T) {
	store := openTestStore(t)
	publisher := &countingPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{PollInterval: time.Hour, BatchSize: 10, Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	relay.Start(context.Background())
	defer relay.Close()
	for i := 0; i < 3; i++ {
		mustAppendMarker(t, store, "s", "d")
		waitPublished(t, publisher, int32(i+1), 2*time.Second)
	}
}

// 启动前积压超过一页：启动后连续排空，不等定时扫描。
func TestRelayDrainsABacklogLargerThanOnePage(t *testing.T) {
	store := openTestStore(t)
	for i := 0; i < 7; i++ {
		mustAppendMarker(t, store, "s", "d")
	}
	publisher := &countingPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{PollInterval: time.Hour, BatchSize: 2, Metrics: newTestMetrics(t)})
	if err != nil {
		t.Fatal(err)
	}
	relay.Start(context.Background())
	defer relay.Close()
	waitPublished(t, publisher, 7, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	if got := publisher.published.Load(); got != 7 {
		t.Fatalf("发布了 %d 条，期望恰好 7 条", got)
	}
}
