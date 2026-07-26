package jobqueue

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
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
	if queue.cfg.AckWait != time.Minute || queue.cfg.MaxDeliver != 3 || queue.cfg.MaxAckPending != 1 {
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
		CodePackageId: "pkg", ExecuteAt: executeAt,
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
