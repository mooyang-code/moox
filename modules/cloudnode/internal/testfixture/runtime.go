package testfixture

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/packages/events"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

// StartRuntime starts an ephemeral central EventBus fixture and registers the
// CloudNode stream/KV topology exactly as moox-eventbus would.
func StartRuntime(t *testing.T, cfg config.JetStreamConfig) *jobqueue.Runtime {
	t.Helper()
	port := cfg.Embedded.Port
	if port <= 0 {
		port = freePort(t)
	}
	dir := t.TempDir()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: filepath.Join(dir, "jetstream"), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		t.Fatal("nats fixture not ready")
	}
	cfg.URLs = []string{fmt.Sprintf("nats://127.0.0.1:%d", port)}
	cfg.NATSURL = cfg.URLs[0]
	rt, err := jobqueue.Connect(trpc.BackgroundContext(), cfg)
	if err != nil {
		srv.Shutdown()
		t.Fatal(err)
	}
	nc, err := nats.Connect(cfg.URLs[0])
	if err != nil {
		_ = rt.Close()
		srv.Shutdown()
		t.Fatal(err)
	}
	js, _ := nc.JetStream()
	registry, err := events.DefaultRegistry()
	if err != nil {
		nc.Close()
		_ = rt.Close()
		srv.Shutdown()
		t.Fatal(err)
	}
	family, err := registry.FamilyPattern(events.CloudJobExecutionRequested)
	if err != nil {
		nc.Close()
		_ = rt.Close()
		srv.Shutdown()
		t.Fatal(err)
	}
	_, err = js.AddStream(&nats.StreamConfig{
		Name:      events.CloudJobExecutionRequested.Stream(),
		Subjects:  []string{family},
		Storage:   nats.FileStorage,
		Retention: nats.WorkQueuePolicy,
	})
	if err != nil && !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
		nc.Close()
		_ = rt.Close()
		srv.Shutdown()
		t.Fatal(err)
	}
	_, err = js.CreateKeyValue(&nats.KeyValueConfig{Bucket: config.Default().JobItem.ActiveKVBucket, Storage: nats.FileStorage, History: 1})
	_ = err
	nc.Close()
	rt.SetCloseHook(func() error { srv.Shutdown(); srv.WaitForShutdown(); return nil })
	return rt
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
