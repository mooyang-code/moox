package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const defaultFetchMaxWait = 500 * time.Millisecond

// JobRequestedTopic is retained as a compatibility label; routed publishes
// use ExecSubject so every concrete message topic carries its route identity.
const JobRequestedTopic = "moox.cloudnode.exec.v1.jobitem.s.*.pkg.*.type.*"

// JetStreamQueue implements ExecutionQueue on the centrally managed stream.
type JetStreamQueue struct {
	rt                *Runtime
	client            *jetstream.Client
	cfg               QueueConfig
	mu                sync.Mutex
	fetchMu           sync.Mutex
	inflight          map[string]*jetstream.Delivery
	consumers         map[string]*jetstream.PullConsumer
	closed            bool
	legacyCleanupOnce sync.Once
	legacyCleanupErr  error
}

func NewJetStreamQueue(rt *Runtime, cfg QueueConfig) *JetStreamQueue {
	if cfg.ExecStream == "" {
		cfg.ExecStream = DefaultExecStream
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = 2 * time.Minute
	}
	if cfg.MaxDeliver <= 0 {
		cfg.MaxDeliver = 3
	}
	if cfg.FetchMaxWait <= 0 {
		cfg.FetchMaxWait = defaultFetchMaxWait
	}
	if cfg.DefaultMaxBatch <= 0 {
		cfg.DefaultMaxBatch = 10
	}
	var client *jetstream.Client
	if rt != nil {
		client = rt.Client()
	}
	return &JetStreamQueue{rt: rt, client: client, cfg: cfg, inflight: make(map[string]*jetstream.Delivery), consumers: make(map[string]*jetstream.PullConsumer)}
}

func (q *JetStreamQueue) Publish(ctx context.Context, item *pb.JobItem) (*PublishResult, error) {
	if item == nil {
		return nil, fmt.Errorf("job item is required")
	}
	if strings.TrimSpace(item.GetSpaceId()) == "" || strings.TrimSpace(item.GetJobItemId()) == "" {
		return nil, fmt.Errorf("space_id and job_item_id are required")
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(item)
	if err != nil {
		return nil, err
	}
	now := timestamppb.Now()
	messageID := item.GetJobId()
	if strings.TrimSpace(messageID) == "" {
		messageID = item.GetJobItemId()
	}
	topic := ExecSubject(q.cfg.Naming, item.GetSpaceId(), item.GetCodePackageId(), item.GetJobType())
	msg := &messagepb.MooxMessage{ProtocolVersion: jetstream.ProtocolVersion, MessageId: messageID, Topic: topic, Kind: messagepb.MessageKind_MESSAGE_KIND_COMMAND, Producer: &messagepb.Producer{ServiceName: "moox-cloudnode", InstanceId: "cloudnode"}, SpaceId: item.GetSpaceId(), OccurredAt: now, PublishedAt: now, ContentType: "application/x-protobuf; message=trpc.moox.cloudnode.JobItem", Payload: data}
	ack, err := q.client.Publish(ctx, msg)
	if err != nil {
		return nil, err
	}
	return &PublishResult{Created: !ack.Duplicate, Duplicate: ack.Duplicate, Subject: topic, Stream: ack.Stream, Sequence: ack.Sequence}, nil
}

func (q *JetStreamQueue) ensureConsumer(spaceID, codePackageID, jobType string) (*jetstream.PullConsumer, error) {
	q.legacyCleanupOnce.Do(func() {
		q.legacyCleanupErr = q.client.DeleteConsumer(context.Background(), q.cfg.ExecStream, "cn_exec_all")
	})
	if q.legacyCleanupErr != nil {
		return nil, fmt.Errorf("remove legacy wildcard execution consumer: %w", q.legacyCleanupErr)
	}
	key := spaceID + "\x00" + codePackageID + "\x00" + jobType
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil, fmt.Errorf("execution queue is closed")
	}
	if consumer := q.consumers[key]; consumer != nil {
		return consumer, nil
	}
	consumer, err := q.client.NewPullConsumer(context.Background(), jetstream.ConsumerConfig{Stream: q.cfg.ExecStream, Durable: ConsumerName(spaceID, codePackageID, jobType), FilterSubject: ExecFilterSubject(q.cfg.Naming, spaceID, codePackageID, jobType), AckWait: q.cfg.AckWait, MaxDeliver: q.cfg.MaxDeliver, MaxAckPending: q.cfg.DefaultMaxBatch, FetchMaxWait: q.cfg.FetchMaxWait})
	if err != nil {
		return nil, err
	}
	q.consumers[key] = consumer
	return consumer, nil
}

func (q *JetStreamQueue) Fetch(ctx context.Context, req FetchRequest) ([]Delivery, error) {
	limit := req.Limit
	if limit <= 0 || limit > q.cfg.DefaultMaxBatch {
		limit = q.cfg.DefaultMaxBatch
	}
	q.fetchMu.Lock()
	defer q.fetchMu.Unlock()
	var deliveries []*jetstream.Delivery
	for _, jobType := range compactStrings(req.SupportedJobTypes) {
		consumer, err := q.ensureConsumer(req.SpaceID, req.CodePackageID, jobType)
		if err != nil {
			return nil, err
		}
		batch, err := consumer.Fetch(ctx, limit-len(deliveries))
		deliveries = append(deliveries, batch...)
		if err != nil && len(batch) == 0 && !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return nil, err
		}
		if len(deliveries) >= limit {
			break
		}
	}
	out := make([]Delivery, 0, len(deliveries))
	for _, delivery := range deliveries {
		item := &pb.JobItem{}
		if err := proto.Unmarshal(delivery.Message.GetPayload(), item); err != nil {
			_ = delivery.Term(context.Background())
			continue
		}
		if req.SpaceID != "" && item.GetSpaceId() != req.SpaceID {
			_ = delivery.Nak(context.Background(), time.Second)
			continue
		}
		if req.CodePackageID != "" && item.GetCodePackageId() != req.CodePackageID {
			_ = delivery.Nak(context.Background(), time.Second)
			continue
		}
		if len(req.SupportedJobTypes) > 0 && !contains(req.SupportedJobTypes, item.GetJobType()) {
			_ = delivery.Nak(context.Background(), time.Second)
			continue
		}
		meta := JobItemMessage{SpaceID: item.GetSpaceId(), JobID: item.GetJobId(), JobItemID: item.GetJobItemId(), JobType: item.GetJobType(), CodePackageID: item.GetCodePackageId(), Params: structToMap(item.GetParams()), Priority: item.GetPriority(), SubmittedAt: delivery.Message.GetOccurredAt().AsTime()}
		token := fmt.Sprintf("%s:%d", delivery.Message.GetMessageId(), delivery.ConsumerSeq)
		q.mu.Lock()
		q.inflight[token] = delivery
		q.mu.Unlock()
		out = append(out, Delivery{Message: meta, AttemptNo: int(delivery.DeliveryCount), AckSubject: token, StreamSeq: delivery.StreamSeq, ConsumerSeq: delivery.ConsumerSeq})
	}
	return out, nil
}

func (q *JetStreamQueue) take(token string) *jetstream.Delivery {
	q.mu.Lock()
	defer q.mu.Unlock()
	d := q.inflight[token]
	delete(q.inflight, token)
	return d
}
func (q *JetStreamQueue) Ack(ctx context.Context, token string) error {
	d := q.take(token)
	if d == nil {
		return fmt.Errorf("ack subject not found")
	}
	return d.Ack(ctx)
}
func (q *JetStreamQueue) Nak(ctx context.Context, token string, delay time.Duration) error {
	d := q.take(token)
	if d == nil {
		return fmt.Errorf("ack subject not found")
	}
	return d.Nak(ctx, delay)
}
func (q *JetStreamQueue) Term(ctx context.Context, token string) error {
	d := q.take(token)
	if d == nil {
		return fmt.Errorf("ack subject not found")
	}
	return d.Term(ctx)
}
func (q *JetStreamQueue) InProgress(ctx context.Context, token string) error {
	q.mu.Lock()
	d := q.inflight[token]
	q.mu.Unlock()
	if d == nil {
		return fmt.Errorf("ack subject not found")
	}
	return d.InProgress(ctx)
}
func (q *JetStreamQueue) Close() error {
	q.fetchMu.Lock()
	defer q.fetchMu.Unlock()
	q.mu.Lock()
	q.closed = true
	consumers := q.consumers
	q.consumers = nil
	q.mu.Unlock()
	var closeErr error
	for _, consumer := range consumers {
		closeErr = errors.Join(closeErr, consumer.Close())
	}
	return closeErr
}

func compactStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
