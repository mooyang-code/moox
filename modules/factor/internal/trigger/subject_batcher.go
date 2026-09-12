package trigger

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

var ErrSubjectBatcherStopped = errors.New("subject batcher stopped")

type SubjectEvent struct {
	SpaceID string
	EventID string
	Ready   *storagepb.ViewSourceSubjectReady
}

type SubjectBatchConfig struct {
	Window           time.Duration `yaml:"window"`
	MaxBatch         int           `yaml:"max_batch"`
	QueueCapacity    int           `yaml:"queue_capacity"`
	ExecutionTimeout time.Duration `yaml:"execution_timeout"`
}

func DefaultSubjectBatchConfig() SubjectBatchConfig {
	return SubjectBatchConfig{Window: 200 * time.Millisecond, MaxBatch: 64, QueueCapacity: 256, ExecutionTimeout: 2 * time.Minute}
}

func (cfg SubjectBatchConfig) Validate() error {
	if cfg.Window <= 0 || cfg.MaxBatch <= 0 || cfg.QueueCapacity <= 0 || cfg.ExecutionTimeout <= 0 {
		return fmt.Errorf("subject batch limits and timeouts must be positive")
	}
	return nil
}

type subjectSubmission struct {
	ctx   context.Context
	event SubjectEvent
	done  chan error
}

// SubjectBatcher bounds queued deliveries and executes compatible microbatches
// serially. The executor can parallelize Python work after its bulk input read.
type SubjectBatcher struct {
	cfg     SubjectBatchConfig
	execute func(context.Context, []SubjectEvent) error
	queue   chan subjectSubmission
	stopped chan struct{}
	started atomic.Bool
}

func NewSubjectBatcher(cfg SubjectBatchConfig, execute func(context.Context, []SubjectEvent) error) (*SubjectBatcher, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if execute == nil {
		return nil, fmt.Errorf("subject batcher requires an executor")
	}
	return &SubjectBatcher{cfg: cfg, execute: execute, queue: make(chan subjectSubmission, cfg.QueueCapacity), stopped: make(chan struct{})}, nil
}

// Submit returns success only after execution succeeds, never after enqueueing.
// The delivery owner must retain/renew its JetStream lease until this returns.
func (b *SubjectBatcher) Submit(ctx context.Context, event SubjectEvent) error {
	if ctx == nil {
		return fmt.Errorf("subject submission context is required")
	}
	p := event.Ready
	if event.SpaceID == "" || event.EventID == "" || p.GetSourceViewId() == "" || p.GetSourceDatasetId() == "" || p.GetSubjectId() == "" || p.GetFrequency() == "" || p.GetPeriodTime() <= 0 || p.GetActiveIndexId() == "" || p.GetInputContractVersion() == "" || p.GetSourceEventId() == "" || p.GetSourceNodeId() == "" || p.GetSourceStoreId() == "" || p.GetSourceSequence() == 0 {
		return fmt.Errorf("subject readiness identity is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-b.stopped:
		return ErrSubjectBatcherStopped
	default:
	}
	event.Ready = proto.Clone(p).(*storagepb.ViewSourceSubjectReady)
	item := subjectSubmission{ctx: ctx, event: event, done: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.stopped:
		return ErrSubjectBatcherStopped
	case b.queue <- item:
	}
	select {
	case err := <-item.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-b.stopped:
		return ErrSubjectBatcherStopped
	}
}

func (b *SubjectBatcher) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("subject batcher context is required")
	}
	if !b.started.CompareAndSwap(false, true) {
		return fmt.Errorf("subject batcher may only run once")
	}
	defer close(b.stopped)
	for {
		var first subjectSubmission
		select {
		case <-ctx.Done():
			return ctx.Err()
		case first = <-b.queue:
		}
		items := []subjectSubmission{first}
		timer := time.NewTimer(b.cfg.Window)
	collect:
		for len(items) < b.cfg.MaxBatch {
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case item := <-b.queue:
				items = append(items, item)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()
		b.runGroups(ctx, items)
	}
}

type subjectBatchKey struct {
	space, view, dataset, frequency, index, contract, tag string
	period                                                int64
}

func (b *SubjectBatcher) runGroups(ctx context.Context, items []subjectSubmission) {
	groups := make(map[subjectBatchKey][]subjectSubmission)
	var order []subjectBatchKey
	for _, item := range items {
		if err := item.ctx.Err(); err != nil {
			item.done <- err
			continue
		}
		p := item.event.Ready
		key := subjectBatchKey{item.event.SpaceID, p.SourceViewId, p.SourceDatasetId, p.Frequency, p.ActiveIndexId, p.InputContractVersion, p.SeriesTag, p.PeriodTime}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], item)
	}
	for _, key := range order {
		group := groups[key]
		batch := make([]SubjectEvent, 0, len(group))
		active := make([]subjectSubmission, 0, len(group))
		for _, item := range group {
			if err := item.ctx.Err(); err != nil {
				item.done <- err
				continue
			}
			active = append(active, item)
			batch = append(batch, item.event)
		}
		if len(batch) == 0 {
			continue
		}
		var err error
		if err = ctx.Err(); err == nil {
			executionCtx, cancel := context.WithTimeout(ctx, b.cfg.ExecutionTimeout)
			err = b.execute(executionCtx, batch)
			if err == nil {
				err = executionCtx.Err()
			}
			cancel()
		}
		for _, item := range active {
			item.done <- err
		}
	}
}
