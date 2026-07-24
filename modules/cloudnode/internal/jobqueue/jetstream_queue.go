package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	nats "github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	trpc "trpc.group/trpc-go/trpc-go"
)

const defaultFetchMaxWait = 500 * time.Millisecond

// JetStreamQueue implements ExecutionQueue on the centrally managed stream.
type JetStreamQueue struct {
	rt         *Runtime
	client     *jetstream.Client
	publisher  *events.Publisher
	cfg        QueueConfig
	mu         sync.Mutex
	inflight   map[string]*jetstream.Delivery
	consumers  map[string]*jetstream.PullConsumer
	fetchLock  map[string]*sync.Mutex
	fetchStart map[string]uint64
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
	var publisher *events.Publisher
	if client != nil {
		registry, err := events.DefaultRegistry()
		if err == nil {
			publisher, _ = events.NewPublisher(client, registry)
		}
	}
	return &JetStreamQueue{rt: rt, client: client, publisher: publisher, cfg: cfg, inflight: make(map[string]*jetstream.Delivery), consumers: make(map[string]*jetstream.PullConsumer), fetchLock: make(map[string]*sync.Mutex), fetchStart: make(map[string]uint64)}
}

func (q *JetStreamQueue) Publish(ctx context.Context, item *pb.JobItem) (*PublishResult, error) {
	if item == nil {
		return nil, fmt.Errorf("job item is required")
	}
	if strings.TrimSpace(item.GetSpaceId()) == "" || strings.TrimSpace(item.GetJobItemId()) == "" {
		return nil, fmt.Errorf("space_id and job_item_id are required")
	}
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(item)
	if err != nil {
		return nil, err
	}
	messageID := item.GetJobItemId()
	values := map[string]any{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	payload, err := structpb.NewStruct(values)
	if err != nil {
		return nil, err
	}
	if q.publisher == nil {
		return nil, errors.New("cloudnode event publisher is unavailable")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	subject, err := registry.RenderSubject(events.CloudJobRequested, item.GetSpaceId(), item.GetJobItemId())
	if err != nil {
		return nil, err
	}
	ack, err := q.publisher.Publish(ctx, events.CloudJobRequested, payload, events.PublishOptions{EventID: messageID, OccurredAt: time.Now().UTC(), SpaceID: item.GetSpaceId(), SubjectID: item.GetJobItemId()})
	if err != nil {
		return nil, err
	}
	return &PublishResult{Created: !ack.Duplicate, Duplicate: ack.Duplicate, Subject: subject, Stream: ack.Stream, Sequence: ack.Sequence}, nil
}

func consumerConfigForRoute(cfg QueueConfig, spaceID, codePackageID, jobType string) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Stream:        qExecStream(cfg),
		Durable:       ConsumerName(spaceID, codePackageID, jobType),
		FilterSubject: ExecFilterSubject(cfg.Naming, spaceID, codePackageID, jobType),
		AckWait:       cfg.AckWait, MaxDeliver: cfg.MaxDeliver, MaxAckPending: cfg.DefaultMaxBatch,
		FetchMaxWait: cfg.FetchMaxWait,
	}
}

func qExecStream(cfg QueueConfig) string {
	if value := strings.TrimSpace(cfg.ExecStream); value != "" {
		return value
	}
	return DefaultExecStream
}

func routeConsumerKey(spaceID, codePackageID, jobType string) string {
	return ConsumerName(spaceID, codePackageID, jobType)
}

func (q *JetStreamQueue) ensureConsumer(spaceID, codePackageID, jobType string) (*jetstream.PullConsumer, error) {
	key := routeConsumerKey(spaceID, codePackageID, jobType)
	if consumer := q.consumers[key]; consumer != nil {
		return consumer, nil
	}
	// The subscription is shared by many SCF poll requests. Binding it to the
	// request context would close the durable subscription as soon as the first
	// request returns.
	consumerCfg := consumerConfigForRoute(q.cfg, spaceID, codePackageID, jobType)
	consumer, err := q.client.NewPullConsumer(trpc.BackgroundContext(), consumerCfg)
	if err != nil {
		return nil, err
	}
	q.consumers[key] = consumer
	return consumer, nil
}

func (q *JetStreamQueue) ensureFetchLock(key string) *sync.Mutex {
	if q.fetchLock == nil {
		q.fetchLock = make(map[string]*sync.Mutex)
	}
	if lock := q.fetchLock[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	q.fetchLock[key] = lock
	return lock
}

func fetchRouteConsumer(ctx context.Context, consumer *jetstream.PullConsumer, limit int) ([]*jetstream.Delivery, error) {
	deliveries, err := consumer.Fetch(ctx, limit)
	if errors.Is(err, nats.ErrTimeout) && len(deliveries) == 0 {
		return nil, nil
	}
	return deliveries, err
}

func tryAcquireFetchLock(lock *sync.Mutex) bool {
	return lock != nil && lock.TryLock()
}

func (q *JetStreamQueue) Fetch(ctx context.Context, req FetchRequest) ([]Delivery, error) {
	if strings.TrimSpace(req.SpaceID) == "" || strings.TrimSpace(req.CodePackageID) == "" {
		return nil, fmt.Errorf("space_id and code_package_id are required")
	}
	jobTypes := uniqueStrings(req.SupportedJobTypes)
	if len(jobTypes) == 0 {
		return nil, fmt.Errorf("supported_job_types is required")
	}
	limit := req.Limit
	if limit <= 0 || limit > q.cfg.DefaultMaxBatch {
		limit = q.cfg.DefaultMaxBatch
	}
	jobTypes = q.orderedJobTypes(req.SpaceID, req.CodePackageID, jobTypes)

	var deliveries []*jetstream.Delivery
	for _, jobType := range jobTypes {
		if len(deliveries) >= limit {
			break
		}
		key := routeConsumerKey(req.SpaceID, req.CodePackageID, jobType)
		q.mu.Lock()
		consumer, err := q.ensureConsumer(req.SpaceID, req.CodePackageID, jobType)
		fetchLock := q.ensureFetchLock(key)
		q.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if !tryAcquireFetchLock(fetchLock) {
			continue
		}
		batch, err := fetchRouteConsumer(ctx, consumer, limit-len(deliveries))
		fetchLock.Unlock()
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, batch...)
	}

	out := make([]Delivery, 0, len(deliveries))
	for _, delivery := range deliveries {
		actionCtx := ctx
		registry, err := events.DefaultRegistry()
		if err != nil {
			return nil, err
		}
		message, payload, decodeErr := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
		if decodeErr != nil {
			if actionErr := delivery.Term(actionCtx); actionErr != nil {
				return nil, errors.Join(decodeErr, actionErr)
			}
			continue
		}
		structPayload, ok := payload.(*structpb.Struct)
		if !ok {
			if actionErr := delivery.Term(actionCtx); actionErr != nil {
				return nil, errors.Join(errors.New("cloudnode payload type mismatch"), actionErr)
			}
			continue
		}
		item := &pb.JobItem{}
		itemJSON, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(structPayload)
		unmarshalErr := protojson.UnmarshalOptions{DiscardUnknown: false}.Unmarshal(itemJSON, item)
		if err != nil || unmarshalErr != nil {
			if actionErr := delivery.Term(actionCtx); actionErr != nil {
				return nil, errors.Join(errors.New("decode malformed cloudnode job item"), fmt.Errorf("term malformed job item: %w", actionErr))
			}
			continue
		}
		if item.GetSpaceId() != req.SpaceID || item.GetCodePackageId() != req.CodePackageID || !contains(req.SupportedJobTypes, item.GetJobType()) {
			if actionErr := delivery.Nak(actionCtx, time.Second); actionErr != nil {
				return nil, fmt.Errorf("nak unsupported job item: %w", actionErr)
			}
			continue
		}
		submittedAt := time.Now().UTC()
		if message.GetOccurredAt() != nil {
			submittedAt = message.GetOccurredAt().AsTime().UTC()
		}
		meta := JobItemMessage{SpaceID: item.GetSpaceId(), JobID: item.GetJobId(), JobItemID: item.GetJobItemId(), JobType: item.GetJobType(), CodePackageID: item.GetCodePackageId(), Params: structToMap(item.GetParams()), Priority: item.GetPriority(), SubmittedAt: submittedAt}
		token := fmt.Sprintf("%s:%d", delivery.RawMessageID, delivery.ConsumerSeq)
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
	q.mu.Lock()
	consumers := q.consumers
	q.consumers = make(map[string]*jetstream.PullConsumer)
	q.fetchLock = make(map[string]*sync.Mutex)
	q.fetchStart = make(map[string]uint64)
	q.mu.Unlock()
	var closeErr error
	for _, consumer := range consumers {
		if err := consumer.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	return closeErr
}

func uniqueStrings(values []string) []string {
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

func rotateStrings(values []string, start int) []string {
	if len(values) < 2 || start%len(values) == 0 {
		return values
	}
	start %= len(values)
	out := make([]string, 0, len(values))
	out = append(out, values[start:]...)
	return append(out, values[:start]...)
}

func (q *JetStreamQueue) orderedJobTypes(spaceID, codePackageID string, jobTypes []string) []string {
	if len(jobTypes) < 2 {
		return jobTypes
	}
	canonical := append([]string(nil), jobTypes...)
	sort.Strings(canonical)
	key := fmt.Sprintf("%q|%q|%q", spaceID, codePackageID, canonical)
	q.mu.Lock()
	start := int(q.fetchStart[key] % uint64(len(jobTypes)))
	q.fetchStart[key]++
	q.mu.Unlock()
	return rotateStrings(canonical, start)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
