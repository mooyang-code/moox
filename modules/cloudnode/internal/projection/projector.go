package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/nats-io/nats.go"
)

const defaultProjectionBatchSize = 100

// ProjectorOptions configures the durable projection consumer.
type ProjectorOptions struct {
	Naming           jobqueue.NamingConfig
	ProjectionStream string
	BatchSize        int
	MaxWait          time.Duration
}

// Projector consumes durable projection events and writes SQLite in batches.
type Projector struct {
	js   nats.JetStreamContext
	repo *Repository
	opts ProjectorOptions
	mu   sync.Mutex
	sub  *nats.Subscription
}

// NewProjector creates a projection event worker.
func NewProjector(js nats.JetStreamContext, repo *Repository, opts ProjectorOptions) *Projector {
	if opts.Naming.SubjectPrefix == "" {
		opts.Naming.SubjectPrefix = jobqueue.DefaultSubjectPrefix
	}
	if opts.ProjectionStream == "" {
		opts.ProjectionStream = jobqueue.DefaultProjectionStream
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultProjectionBatchSize
	}
	if opts.MaxWait <= 0 {
		opts.MaxWait = 500 * time.Millisecond
	}
	return &Projector{js: js, repo: repo, opts: opts}
}

// PublishReported appends a JobItem reported event to the projection stream.
func (p *Projector) PublishReported(ctx context.Context, event ReportEvent) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal reported event: %w", err)
	}
	_, err = p.js.Publish(
		jobqueue.ProjectionSubject(p.opts.Naming, jobqueue.ProjectionEventJobItemReported),
		raw,
		nats.Context(ctx),
	)
	if err != nil {
		return fmt.Errorf("publish reported event: %w", err)
	}
	return nil
}

// RunOnce drains at most one batch of reported events.
func (p *Projector) RunOnce(ctx context.Context) error {
	if p == nil || p.js == nil || p.repo == nil {
		return nil
	}
	subject := jobqueue.ProjectionSubject(p.opts.Naming, jobqueue.ProjectionEventJobItemReported)
	sub, err := p.subscription(subject)
	if err != nil {
		return fmt.Errorf("projection subscribe: %w", err)
	}
	msgs, err := sub.Fetch(p.opts.BatchSize, nats.MaxWait(p.opts.MaxWait))
	if err != nil {
		if err == nats.ErrTimeout {
			return nil
		}
		return fmt.Errorf("projection fetch: %w", err)
	}
	events := make([]ReportEvent, 0, len(msgs))
	for _, msg := range msgs {
		var event ReportEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			_ = msg.Term()
			return fmt.Errorf("decode reported event: %w", err)
		}
		events = append(events, event)
	}
	if err := p.repo.MarkReportedBatch(ctx, events); err != nil {
		for _, msg := range msgs {
			_ = msg.NakWithDelay(time.Second)
		}
		return err
	}
	for _, msg := range msgs {
		_ = msg.Ack()
	}
	return nil
}

func (p *Projector) subscription(subject string) (*nats.Subscription, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sub != nil && p.sub.IsValid() {
		return p.sub, nil
	}
	sub, err := p.js.PullSubscribe(
		subject,
		"cn_projection_jobitem_reported",
		nats.BindStream(p.opts.ProjectionStream),
		nats.ManualAck(),
		nats.AckExplicit(),
	)
	if err != nil {
		return nil, err
	}
	p.sub = sub
	return sub, nil
}

// Run continuously drains projection events until context cancellation.
func (p *Projector) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			_ = p.RunOnce(ctx)
		}
	}
}
