package outbox

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// EventBus 连不上时 Outbox 转发照常启动、能随时停下，启动不被连接挡住。
func TestStartWithEventBusStartsWithoutEventBus(t *testing.T) {
	store, err := pebble.Open(pebble.Options{NodeID: "storage-primary", Path: filepath.Join(t.TempDir(), "pebble")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Now()
	stop, err := StartWithEventBus(context.Background(), store, jetstream.Config{URLs: []string{"nats://127.0.0.1:1"}, Name: "storage-primary-test"})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("startOutboxRelay blocked for %s", elapsed)
	}
	time.Sleep(200 * time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("outbox relay did not stop while EventBus was unavailable")
	}
}

// EventBus 先不可用、后来起来：启动时的首次连接失败后，积压由转发协程在连续发布失败后的重连发出，并从 Outbox 删除。
func TestStartWithEventBusDrainsBacklogAfterEventBusComesUp(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	store, err := pebble.Open(pebble.Options{NodeID: "storage-primary", Path: filepath.Join(t.TempDir(), "pebble")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}},
	}
	if _, err := store.UpsertFields(ctx, "quant", "prices", "", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	reconnectsBefore := observability.DefaultViewMetrics.Snapshot().OutboxReconnectSuccesses
	config := jetstream.Config{URLs: []string{fmt.Sprintf("nats://127.0.0.1:%d", port)}, Name: "storage-primary-test", ConnectTimeout: 200 * time.Millisecond}
	stop, err := StartWithEventBus(ctx, store, config)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// 等启动时的那次连接超时失败，之后能连上只可能靠转发协程的重连。
	time.Sleep(time.Second)
	if entries, err := store.ListOutbox(ctx, 0, 10); err != nil || len(entries) != 1 {
		t.Fatalf("outbox before EventBus is up = %d entries, err=%v; want the backlog kept", len(entries), err)
	}

	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("内嵌 NATS 没有启动")
	}
	defer server.Shutdown()
	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.>"}, Storage: nats.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		entries, err := store.ListOutbox(ctx, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("EventBus 起来后 Outbox 积压没有发出")
		}
		time.Sleep(100 * time.Millisecond)
	}
	info, err := js.StreamInfo("MOOX_STORAGE")
	if err != nil || info.State.Msgs != 1 {
		t.Fatalf("stream messages=%v err=%v, want 1", info, err)
	}
	if got := observability.DefaultViewMetrics.Snapshot().OutboxReconnectSuccesses; got <= reconnectsBefore {
		t.Fatalf("outbox reconnect successes=%d before=%d, want the relay to reconnect", got, reconnectsBefore)
	}
}
