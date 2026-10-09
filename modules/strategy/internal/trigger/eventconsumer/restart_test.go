package eventconsumer

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// unusedLoader 在没有启用实例的测试里不会被调用。
type unusedLoader struct{}

func (unusedLoader) LoadBar(context.Context, string, input.Resolved, *dsl.Program, input.Bar) (input.Loaded, error) {
	return input.Loaded{}, errors.New("测试中不应读取输入")
}

// encodedBody 按发布器的方式序列化事件信封。
func encodedBody(encoded events.EncodedEvent) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func startNATS(t *testing.T, port int, dir string) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("NATS 没有就绪")
	}
	return srv
}

func eventually(t *testing.T, timeout time.Duration, message string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(message)
}

// EventBus 重启后（拉取请求收到 Server Shutdown，运行循环退出），消费者必须自行重建并继续处理新事件。
func TestConsumerRecoversAfterEventBusRestart(t *testing.T) {
	port, dir := freePort(t), t.TempDir()
	srv := startNATS(t, port, dir)
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	admin, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	js, err := admin.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.>"}, Storage: nats.FileStorage}); err != nil {
		t.Fatal(err)
	}
	admin.Close()

	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{srv.ClientURL()}, Name: "strategy-consumer-restart"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	consumer := New(ConsumerConfig{Connect: func(context.Context) (*jetstream.Client, error) { return client, nil }, ConsumerName: "strategy-restart-test", FetchMaxWait: 200 * time.Millisecond, Logf: t.Logf}, &trigger.Handler{Store: repo, Loader: unusedLoader{}})
	if err := consumer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Close() })
	eventually(t, 10*time.Second, "消费者应先就绪", consumer.Ready)

	srv.Shutdown()
	srv.WaitForShutdown()
	eventually(t, 10*time.Second, "EventBus 停机后消费循环应退出", func() bool { return !consumer.Ready() })
	srv = startNATS(t, port, dir)
	eventually(t, 40*time.Second, "EventBus 恢复后消费者应自行重建", consumer.Ready)

	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	payload := &storagepb.ViewDataReady{
		ViewId: "view_a", Frequency: "1h", PeriodTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix(), Status: "complete", ReadyAt: timestamppb.Now(),
		CompletionEventId: "collector-1", ViewConfigId: "cfg-1", DatasetId: "ds", VisibleScope: "view:view_a", CompletionKind: events.CollectorPeriodCompleted.Name(),
	}
	encoded, err := registry.Encode(events.ViewDataReady, payload, events.PublishOptions{EventID: "ready-after-restart", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "view_a"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := encodedBody(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PublishRaw(ctx, encoded.Subject, "ready-after-restart", body, events.ContentType); err != nil {
		t.Fatal(err)
	}
	monitor, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	monitorJS, err := monitor.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "重建后的消费者应处理并确认新事件", func() bool {
		info, err := monitorJS.ConsumerInfo("MOOX_STORAGE", "strategy-restart-test")
		return err == nil && info.Delivered.Consumer > 0 && info.NumAckPending == 0 && info.NumPending == 0
	})
}

// EventBus 在策略启动时不可用：进程照常启动、消费者未就绪；EventBus 恢复后自行连接并就绪。关闭时等监督循环退出。
func TestConsumerStartsWithoutEventBus(t *testing.T) {
	port, dir := freePort(t), t.TempDir()
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	url := "nats://127.0.0.1:" + strconv.Itoa(port)
	connect := func(ctx context.Context) (*jetstream.Client, error) {
		return jetstream.Connect(ctx, jetstream.Config{URLs: []string{url}, Name: "strategy-consumer-late"})
	}
	consumer := New(ConsumerConfig{Connect: connect, ConsumerName: "strategy-late-test", FetchMaxWait: 200 * time.Millisecond, Logf: t.Logf}, &trigger.Handler{Store: repo, Loader: unusedLoader{}})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatalf("EventBus 不可用时也应能启动：%v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if consumer.Ready() {
		t.Fatal("连上 EventBus 之前不应就绪")
	}
	srv := startNATS(t, port, dir)
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	admin, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	js, err := admin.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.>"}, Storage: nats.FileStorage}); err != nil {
		t.Fatal(err)
	}
	admin.Close()
	eventually(t, 40*time.Second, "EventBus 恢复后消费者应自行连接并就绪", consumer.Ready)
	closed := make(chan error, 1)
	go func() { closed <- consumer.Close() }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("关闭应在监督循环退出后返回")
	}
	if consumer.Ready() {
		t.Fatal("关闭后不应就绪")
	}
}
