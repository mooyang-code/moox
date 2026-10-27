package outbox

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type countingPublisher struct{ published atomic.Int32 }

func (p *countingPublisher) PublishMessage(context.Context, []byte) error {
	p.published.Add(1)
	return nil
}

func upsertTestRow(t *testing.T, store *pebble.Store, id string) {
	t.Helper()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: id, Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
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

// 写入 outbox 后立即发布，不等轮询间隔（轮询设成 1 小时）。
func TestRelayPublishesRightAfterCommitWithoutWaitingForThePoll(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	publisher := &countingPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{PollInterval: time.Hour, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	relay.Start(context.Background())
	defer relay.Close()
	for i := 0; i < 3; i++ {
		upsertTestRow(t, store, fmt.Sprintf("r%d", i))
		waitPublished(t, publisher, int32(i+1), 2*time.Second)
	}
}

// 启动前积压超过一批：启动后连续排空，不等轮询。
func TestRelayDrainsABacklogLargerThanOneBatch(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < 7; i++ {
		upsertTestRow(t, store, fmt.Sprintf("r%d", i))
	}
	publisher := &countingPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{PollInterval: time.Hour, BatchSize: 2})
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
