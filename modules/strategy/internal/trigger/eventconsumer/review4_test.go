package eventconsumer

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

// 连接被永久关闭（nats.go 在连续鉴权失败等情况下会这样做）而服务端一直在线：消费者丢弃关闭的连接重新拨号，
// 而不是用它无限重试、一直不就绪。
func TestConsumerRedialsAfterConnectionClosed(t *testing.T) {
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
	var dials atomic.Int32
	url := srv.ClientURL()
	connect := func(ctx context.Context) (*jetstream.Client, error) {
		dials.Add(1)
		return jetstream.Connect(ctx, jetstream.Config{URLs: []string{url}, Name: "strategy-consumer-redial"})
	}
	consumer := New(ConsumerConfig{Connect: connect, ConsumerName: "strategy-redial-test", FetchMaxWait: 200 * time.Millisecond, Logf: t.Logf}, &trigger.Handler{Store: repo, Loader: unusedLoader{}})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = consumer.Close() })
	eventually(t, 10*time.Second, "消费者应先就绪", consumer.Ready)

	consumer.mu.Lock()
	current := consumer.client
	consumer.mu.Unlock()
	_ = current.Close()
	eventually(t, 10*time.Second, "连接关闭后消费循环应退出", func() bool { return !consumer.Ready() })
	eventually(t, 20*time.Second, "应重新拨号并恢复就绪", func() bool { return dials.Load() >= 2 && consumer.Ready() })
}
