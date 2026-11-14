package relay

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// EventBus 连不上时完成事件转发照常启动、能随时停下，启动不被连接挡住。
func TestStartWithEventBusStartsWithoutEventBus(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "pebble")})
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
		t.Fatalf("启动转发被挡住 %s", elapsed)
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
		t.Fatal("EventBus 不可用时转发协程没有停下")
	}
}

// EventBus 先不可用、后来起来：启动时的首次连接失败后，积压由转发协程在连续发布失败后的重连发出，游标随之推进。
func TestStartWithEventBusDrainsBacklogAfterEventBusComesUp(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "pebble")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := appendTestMarker(ctx, store, "quant", "prices"); err != nil {
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
	if cursor, err := store.MarkerRelayCursor(); err != nil || cursor != 0 {
		t.Fatalf("EventBus 起来之前游标 = %d err=%v，期望积压还在", cursor, err)
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
		cursor, err := store.MarkerRelayCursor()
		if err != nil {
			t.Fatal(err)
		}
		if cursor == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("EventBus 起来后积压的完成事件没有发出")
		}
		time.Sleep(100 * time.Millisecond)
	}
	info, err := js.StreamInfo("MOOX_STORAGE")
	if err != nil || info.State.Msgs != 1 {
		t.Fatalf("stream messages=%v err=%v, want 1", info, err)
	}
	if got := observability.DefaultViewMetrics.Snapshot().OutboxReconnectSuccesses; got <= reconnectsBefore {
		t.Fatalf("重连成功次数=%d，启动前=%d，期望转发协程重连过", got, reconnectsBefore)
	}
}
