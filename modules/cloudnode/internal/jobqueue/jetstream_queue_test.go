package jobqueue

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type captureRawPublisher struct {
	message *eventpb.EventMessage
}

func (p *captureRawPublisher) PublishRaw(_ context.Context, _ string, _ string, raw []byte, _ string) (*jetstream.PublishAck, error) {
	p.message = &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, p.message); err != nil {
		return nil, err
	}
	return &jetstream.PublishAck{}, nil
}

func TestNewJetStreamQueueUsesCodeOwnedDefaults(t *testing.T) {
	queue := NewJetStreamQueue(nil, QueueConfig{})
	if queue.cfg.AckWait != 120*time.Second || queue.cfg.MaxDeliver != 3 || queue.cfg.MaxAckPending != 32 {
		t.Fatalf("config = %+v", queue.cfg)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishCarriesExecuteAtToJobExecutionRequested(t *testing.T) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	capture := &captureRawPublisher{}
	publisher, err := events.NewPublisher(capture, registry)
	if err != nil {
		t.Fatal(err)
	}
	queue := NewJetStreamQueue(nil, QueueConfig{})
	queue.publisher = publisher
	executeAt := timestamppb.New(time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC))
	err = queue.Publish(context.Background(), &pb.JobItem{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline",
		ExecuteAt: executeAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := &cloudjobpb.JobExecutionRequested{}
	if err := proto.Unmarshal(capture.message.GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(payload.GetExecuteAt(), executeAt) {
		t.Fatalf("execute_at = %v, want %v", payload.GetExecuteAt(), executeAt)
	}
}

func TestEnsureJobExecutionQueueUsesConfiguredMaxAckPending(t *testing.T) {
	port := reserveJetStreamPort(t)
	server, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(5 * time.Second) {
		server.Shutdown()
		t.Fatal("nats server not ready")
	}
	t.Cleanup(server.Shutdown)
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime, err := Connect(ctx, config.JetStreamConfig{URLs: []string{url}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.EnsureStreams(config.JetStreamConfig{}, config.JobItemConfig{ActiveKVBucket: "TEST_JOB_ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	raw, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(raw.Close)
	js, err := raw.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	family, err := registry.FamilyPattern(events.CloudJobExecutionRequested)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: events.CloudJobExecutionRequested.Stream(), Subjects: []string{family}, Storage: nats.MemoryStorage,
	}); err != nil {
		t.Fatal(err)
	}
	queue := NewJetStreamQueue(runtime, QueueConfig{MaxDeliver: 3, MaxAckPending: 32})
	identity := cloudjobqueue.Identity{SpaceID: "crypto", JobType: "collect.kline"}
	if err := queue.EnsureJobExecutionQueue(ctx, identity); err != nil {
		t.Fatal(err)
	}
	durable, err := identity.ConsumerName()
	if err != nil {
		t.Fatal(err)
	}
	info, err := js.ConsumerInfo(events.CloudJobExecutionRequested.Stream(), durable)
	if err != nil {
		t.Fatal(err)
	}
	if info.Config.MaxAckPending != 32 {
		t.Fatalf("MaxAckPending = %d, want 32", info.Config.MaxAckPending)
	}
}

func reserveJetStreamPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
