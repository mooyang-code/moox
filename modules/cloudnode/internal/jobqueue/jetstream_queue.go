package jobqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/structpb"
)

const defaultFetchMaxWait = 500 * time.Millisecond

// JetStreamQueue implements ExecutionQueue with a CloudNode-private JetStream stream.
type JetStreamQueue struct {
	rt          *Runtime
	js          nats.JetStreamContext
	cfg         QueueConfig
	mu          sync.Mutex
	inflightMsg map[string]*nats.Msg
	subMu       sync.Mutex
	pullSubs    map[string]*nats.Subscription
}

// NewJetStreamQueue creates a JetStream-backed execution queue.
func NewJetStreamQueue(rt *Runtime, cfg QueueConfig) *JetStreamQueue {
	if cfg.Naming.SubjectPrefix == "" {
		cfg.Naming.SubjectPrefix = DefaultSubjectPrefix
	}
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
	return &JetStreamQueue{
		rt:          rt,
		js:          rt.JetStream(),
		cfg:         cfg,
		inflightMsg: make(map[string]*nats.Msg),
		pullSubs:    make(map[string]*nats.Subscription),
	}
}

func (q *JetStreamQueue) Publish(ctx context.Context, item *pb.JobItem) (*PublishResult, error) {
	if item == nil {
		return nil, fmt.Errorf("job item is required")
	}
	msg, err := jobItemMessage(item)
	if err != nil {
		return nil, err
	}
	subject := ExecSubject(q.cfg.Naming, msg.SpaceID, msg.CodePackageID, msg.JobType)
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal job item message: %w", err)
	}
	natsMsg := &nats.Msg{
		Subject: subject,
		Header:  nats.Header{},
		Data:    raw,
	}
	natsMsg.Header.Set(nats.MsgIdHdr, msg.SpaceID+":"+msg.JobItemID)
	pubAck, err := q.js.PublishMsg(natsMsg, nats.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("publish job item %s: %w", msg.JobItemID, err)
	}
	return &PublishResult{
		Created:   !pubAck.Duplicate,
		Duplicate: pubAck.Duplicate,
		Subject:   subject,
		Stream:    pubAck.Stream,
		Sequence:  pubAck.Sequence,
	}, nil
}

func (q *JetStreamQueue) Fetch(ctx context.Context, req FetchRequest) ([]Delivery, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = q.cfg.DefaultMaxBatch
	}
	if limit > q.cfg.DefaultMaxBatch {
		limit = q.cfg.DefaultMaxBatch
	}
	jobTypes := compactStrings(req.SupportedJobTypes)
	deliveries := make([]Delivery, 0, limit)
	for _, jobType := range jobTypes {
		if len(deliveries) >= limit {
			break
		}
		filter := ExecFilterSubject(q.cfg.Naming, req.SpaceID, req.CodePackageID, jobType)
		durable := ConsumerName(req.SpaceID, req.CodePackageID, jobType)
		sub, err := q.pullSubscription(filter, durable)
		if err != nil {
			return nil, fmt.Errorf("pull subscribe %s: %w", filter, err)
		}
		remaining := limit - len(deliveries)
		msgs, err := sub.Fetch(remaining, nats.MaxWait(q.cfg.FetchMaxWait))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			return nil, fmt.Errorf("fetch %s: %w", filter, err)
		}
		for _, msg := range msgs {
			delivery, err := q.deliveryFromMsg(msg)
			if err != nil {
				_ = msg.Term()
				return nil, err
			}
			q.rememberInflight(msg)
			deliveries = append(deliveries, delivery)
		}
	}
	return deliveries, nil
}

func (q *JetStreamQueue) Ack(ctx context.Context, ackSubject string) error {
	return q.ack(ctx, ackSubject, func(msg *nats.Msg) error { return msg.Ack() }, []byte("+ACK"))
}

func (q *JetStreamQueue) Nak(ctx context.Context, ackSubject string, delay time.Duration) error {
	if msg := q.takeInflight(ackSubject); msg != nil {
		if delay > 0 {
			return msg.NakWithDelay(delay)
		}
		return msg.Nak()
	}
	return q.publishAck(ctx, ackSubject, []byte("-NAK"))
}

func (q *JetStreamQueue) Term(ctx context.Context, ackSubject string) error {
	return q.ack(ctx, ackSubject, func(msg *nats.Msg) error { return msg.Term() }, []byte("+TERM"))
}

func (q *JetStreamQueue) InProgress(ctx context.Context, ackSubject string) error {
	return q.ack(ctx, ackSubject, func(msg *nats.Msg) error { return msg.InProgress() }, []byte("+WPI"))
}

func (q *JetStreamQueue) Close() error {
	q.subMu.Lock()
	defer q.subMu.Unlock()
	for key, sub := range q.pullSubs {
		_ = sub.Unsubscribe()
		delete(q.pullSubs, key)
	}
	return nil
}

func (q *JetStreamQueue) pullSubscription(filter string, durable string) (*nats.Subscription, error) {
	key := filter + "\x00" + durable
	q.subMu.Lock()
	defer q.subMu.Unlock()
	if sub := q.pullSubs[key]; sub != nil && sub.IsValid() {
		return sub, nil
	}
	sub, err := q.js.PullSubscribe(
		filter,
		durable,
		nats.BindStream(q.cfg.ExecStream),
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.AckWait(q.cfg.AckWait),
		nats.MaxDeliver(q.cfg.MaxDeliver),
	)
	if err != nil {
		return nil, err
	}
	q.pullSubs[key] = sub
	return sub, nil
}

func (q *JetStreamQueue) ack(ctx context.Context, ackSubject string, byMsg func(*nats.Msg) error, fallback []byte) error {
	if msg := q.takeInflight(ackSubject); msg != nil {
		return byMsg(msg)
	}
	return q.publishAck(ctx, ackSubject, fallback)
}

func (q *JetStreamQueue) publishAck(ctx context.Context, ackSubject string, payload []byte) error {
	ackSubject = strings.TrimSpace(ackSubject)
	if ackSubject == "" {
		return fmt.Errorf("ack subject is required")
	}
	if q.rt == nil || q.rt.Conn() == nil {
		return fmt.Errorf("nats connection is not initialized")
	}
	done := make(chan error, 1)
	go func() {
		done <- q.rt.Conn().Publish(ackSubject, payload)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (q *JetStreamQueue) deliveryFromMsg(msg *nats.Msg) (Delivery, error) {
	var payload JobItemMessage
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		return Delivery{}, fmt.Errorf("decode job item message: %w", err)
	}
	meta, err := msg.Metadata()
	if err != nil {
		return Delivery{}, fmt.Errorf("read jetstream metadata: %w", err)
	}
	return Delivery{
		Message:     payload,
		AttemptNo:   int(meta.NumDelivered),
		AckSubject:  msg.Reply,
		StreamSeq:   meta.Sequence.Stream,
		ConsumerSeq: meta.Sequence.Consumer,
	}, nil
}

func (q *JetStreamQueue) rememberInflight(msg *nats.Msg) {
	if msg == nil || strings.TrimSpace(msg.Reply) == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inflightMsg[msg.Reply] = msg
}

func (q *JetStreamQueue) takeInflight(ackSubject string) *nats.Msg {
	q.mu.Lock()
	defer q.mu.Unlock()
	msg := q.inflightMsg[ackSubject]
	delete(q.inflightMsg, ackSubject)
	return msg
}

func jobItemMessage(item *pb.JobItem) (JobItemMessage, error) {
	msg := JobItemMessage{
		SpaceID:       strings.TrimSpace(item.GetSpaceId()),
		JobID:         strings.TrimSpace(item.GetJobId()),
		JobItemID:     strings.TrimSpace(item.GetJobItemId()),
		JobType:       strings.TrimSpace(item.GetJobType()),
		CodePackageID: strings.TrimSpace(item.GetCodePackageId()),
		Params:        structToMap(item.GetParams()),
		Priority:      item.GetPriority(),
		SubmittedAt:   time.Now().UTC(),
	}
	switch {
	case msg.SpaceID == "":
		return JobItemMessage{}, fmt.Errorf("space_id is required")
	case msg.JobID == "":
		return JobItemMessage{}, fmt.Errorf("job_id is required")
	case msg.JobItemID == "":
		return JobItemMessage{}, fmt.Errorf("job_item_id is required")
	case msg.JobType == "":
		return JobItemMessage{}, fmt.Errorf("job_type is required")
	case msg.CodePackageID == "":
		return JobItemMessage{}, fmt.Errorf("code_package_id is required")
	default:
		return msg, nil
	}
}

func structToMap(st *structpb.Struct) map[string]any {
	if st == nil {
		return map[string]any{}
	}
	return st.AsMap()
}

func mapToStruct(values map[string]any) *structpb.Struct {
	st, err := structpb.NewStruct(values)
	if err != nil {
		return &structpb.Struct{}
	}
	return st
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
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
