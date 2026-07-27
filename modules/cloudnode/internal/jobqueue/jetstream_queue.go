package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

// DefaultAckWait leaves enough time for the Collector's bounded workload and status reports.
const DefaultAckWait = 120 * time.Second

type JetStreamQueue struct {
	rt        *Runtime
	client    *jetstream.Client
	registry  *events.Registry
	publisher *events.Publisher
	cfg       QueueConfig
}

func NewJetStreamQueue(rt *Runtime, cfg QueueConfig) *JetStreamQueue {
	if cfg.AckWait <= 0 {
		cfg.AckWait = DefaultAckWait
	}
	if cfg.MaxDeliver <= 0 {
		cfg.MaxDeliver = 3
	}
	if cfg.MaxAckPending <= 0 {
		cfg.MaxAckPending = 32
	}
	var client *jetstream.Client
	if rt != nil {
		client = rt.Client()
	}
	registry, registryErr := events.DefaultRegistry()
	var publisher *events.Publisher
	if client != nil && registryErr == nil {
		publisher, _ = events.NewPublisher(client, registry)
	}
	return &JetStreamQueue{rt: rt, client: client, registry: registry, publisher: publisher, cfg: cfg}
}

func (q *JetStreamQueue) EnsureJobExecutionQueue(ctx context.Context, identity cloudjobqueue.Identity) error {
	if q.client == nil || q.registry == nil {
		return errors.New("cloudnode event consumer is unavailable")
	}
	name, err := identity.ConsumerName()
	if err != nil {
		return err
	}
	subjectID, err := identity.SubjectID()
	if err != nil {
		return err
	}
	info, err := events.EnsureSubjectConsumer(ctx, q.client, q.registry, events.SubjectConsumerConfig{
		ConsumerConfig: events.ConsumerConfig{
			Name: name, Event: events.CloudJobExecutionRequested,
			AckWait: q.cfg.AckWait, MaxDeliver: q.cfg.MaxDeliver, MaxAckPending: q.cfg.MaxAckPending,
			FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
		},
		SpaceID: identity.SpaceID, SubjectID: subjectID,
	})
	if err != nil {
		return err
	}
	if info.MaxDeliver <= 0 {
		return errors.New("job execution queue max_deliver must be positive")
	}
	return nil
}

func (q *JetStreamQueue) Publish(ctx context.Context, item *pb.JobItem) error {
	if item == nil {
		return fmt.Errorf("job item is required")
	}
	if strings.TrimSpace(item.GetSpaceId()) == "" || strings.TrimSpace(item.GetJobItemId()) == "" {
		return fmt.Errorf("space_id and job_item_id are required")
	}
	subjectID, err := (cloudjobqueue.Identity{SpaceID: item.GetSpaceId(), JobType: item.GetJobType()}).SubjectID()
	if err != nil {
		return err
	}
	if q.publisher == nil {
		return errors.New("cloudnode event publisher is unavailable")
	}
	payload := &cloudjobpb.JobExecutionRequested{
		JobId: item.GetJobId(), JobItemId: item.GetJobItemId(), JobType: item.GetJobType(),
		Params: item.GetParams(), Priority: item.GetPriority(), ExecuteAt: item.GetExecuteAt(),
	}
	_, err = q.publisher.Publish(ctx, events.CloudJobExecutionRequested, payload, events.PublishOptions{
		EventID: item.GetJobItemId(), OccurredAt: time.Now().UTC(), SpaceID: item.GetSpaceId(), SubjectID: subjectID,
	})
	return err
}

func (q *JetStreamQueue) Close() error {
	if q == nil || q.rt == nil {
		return nil
	}
	return q.rt.Close()
}
