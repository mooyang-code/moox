package eventpublisher

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
)

func TestNewRejectsMissingEventBusConfig(t *testing.T) {
	if _, err := New(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), "agent"); err == nil {
		t.Fatal("missing EventBus config was accepted")
	}
}

func TestUninitializedPublisherRejectsHostMetric(t *testing.T) {
	var publisher *JetStreamPublisher
	if err := publisher.PublishHostMetric(context.Background(), "message", &hostmetricpb.HostMetric{}, time.Now()); err == nil {
		t.Fatal("uninitialized publisher accepted a metric")
	}
	if publisher.Ready() {
		t.Fatal("uninitialized publisher reported ready")
	}
	if err := publisher.Close(); err != nil {
		t.Fatal(err)
	}
}

type blockingRawPublisher struct {
	entered chan struct{}
	release chan struct{}
}

func (p *blockingRawPublisher) PublishRaw(context.Context, string, string, []byte, string) (*jetstream.PublishAck, error) {
	close(p.entered)
	<-p.release
	return &jetstream.PublishAck{}, nil
}

func TestJetStreamPublisherCloseWaitsForPublish(t *testing.T) {
	raw := &blockingRawPublisher{entered: make(chan struct{}), release: make(chan struct{})}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	governed, err := events.NewPublisher(raw, registry)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &JetStreamPublisher{publisher: governed}
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- publisher.PublishHostMetric(
			context.Background(),
			"message",
			&hostmetricpb.HostMetric{AgentId: "agent", Hostname: "host", Snapshot: &hostmetricpb.HostSnapshot{}},
			time.Now(),
		)
	}()
	<-raw.entered

	closeDone := make(chan error, 1)
	go func() { closeDone <- publisher.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while PublishHostMetric was active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(raw.release)
	if err := <-publishDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishHostMetric(
		context.Background(),
		"message-2",
		&hostmetricpb.HostMetric{AgentId: "agent", Hostname: "host", Snapshot: &hostmetricpb.HostSnapshot{}},
		time.Now(),
	); err == nil {
		t.Fatal("closed publisher accepted a metric")
	}
}
