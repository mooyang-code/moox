package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	nats "github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

const ConsumerName = "factor_collector_period_v1"

type ConsumerConfig struct {
	URLs               []string
	CredentialFile     string
	FetchMaxWait       time.Duration
	PeriodBudgetMin    time.Duration
	PeriodBudgetMax    time.Duration
	NakDelay           time.Duration
	MaxAckPending      int
	InProgressInterval time.Duration
	ReadColumnTimeout  time.Duration
}

func (cfg *ConsumerConfig) applyDefaults() {
	if len(cfg.URLs) == 0 {
		cfg.URLs = []string{"nats://127.0.0.1:4222"}
	}
	if cfg.FetchMaxWait <= 0 {
		cfg.FetchMaxWait = time.Second
	}
	if cfg.PeriodBudgetMin <= 0 {
		cfg.PeriodBudgetMin = time.Minute
	}
	if cfg.PeriodBudgetMax <= 0 {
		cfg.PeriodBudgetMax = 15 * time.Minute
	}
	if cfg.PeriodBudgetMin > cfg.PeriodBudgetMax {
		cfg.PeriodBudgetMin = cfg.PeriodBudgetMax
	}
	if cfg.NakDelay <= 0 {
		cfg.NakDelay = 10 * time.Second
	}
	if cfg.MaxAckPending <= 0 {
		cfg.MaxAckPending = 16
	}
	if cfg.InProgressInterval <= 0 {
		cfg.InProgressInterval = 30 * time.Second
	}
	if cfg.ReadColumnTimeout <= 0 {
		cfg.ReadColumnTimeout = 20 * time.Second
	}
}

type ConsumerStatus struct {
	Ready              bool
	FilterSubjects     []string
	FilterRefreshError string
}

// Consumer owns the durable period-completion subscription and its lanes.
// The JetStream durable uses the stable event-family filter; exact enabled-set
// subjects are refreshed locally because JetStream cannot widen a durable's
// FilterSubjects without replacing its acknowledgement state.
type Consumer struct {
	sets     trigger.SetLocator
	handler  *Handler
	consumer *events.Consumer
	client   *jetstream.Client
	runner   *jetstream.Runner

	mu                 sync.RWMutex
	filters            []string
	filterRefreshError string
	cancel             context.CancelFunc
	ready              atomic.Bool
	closeOnce          sync.Once
	runWG              sync.WaitGroup
}

func NewConsumer(ctx context.Context, cfg ConsumerConfig, sets trigger.SetLocator, periodStore trigger.PeriodStore, runner trigger.PipelineRunner, locks trigger.SetLocks) (*Consumer, error) {
	if sets == nil || periodStore == nil || runner == nil {
		return nil, fmt.Errorf("factor trigger set locator, Storage and pipeline runner are required")
	}
	cfg.applyDefaults()
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c := &Consumer{sets: sets}
	if err := c.RefreshFilters(ctx); err != nil {
		return nil, fmt.Errorf("load enabled factor set filters: %w", err)
	}

	clientCfg := jetstream.ConfigFromEnv(append([]string(nil), cfg.URLs...), "moox-factor")
	if cfg.CredentialFile != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
			return nil, err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	ackWait := cfg.PeriodBudgetMax + time.Minute
	consumer, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
		Name: ConsumerName, Event: events.CollectorPeriodCompleted,
		AckWait: ackWait, MaxDeliver: -1, MaxAckPending: cfg.MaxAckPending,
		FetchMaxWait: cfg.FetchMaxWait, DeliverPolicy: nats.DeliverNewPolicy,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	handler := NewHandler(sets, periodStore, runner, locks, HandlerConfig{
		NakDelay: cfg.NakDelay, PeriodBudgetMin: cfg.PeriodBudgetMin,
		PeriodBudgetMax: cfg.PeriodBudgetMax, ReadColumnTimeout: cfg.ReadColumnTimeout,
	})
	runCtx, cancel := context.WithCancel(ctx)
	c.client, c.consumer, c.handler, c.cancel = client, consumer, handler, cancel
	c.runner = jetstream.NewRunner(consumer, handler, jetstream.RunnerConfig{
		BatchSize: cfg.MaxAckPending, IndependentBatch: true,
		InProgressInterval: cfg.InProgressInterval,
		ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			log.ErrorContextf(runCtx, "factor period consumer error: %v", err)
		}),
	})
	c.ready.Store(true)
	c.runWG.Add(1)
	go func() {
		defer c.runWG.Done()
		runUntilCancelled(runCtx, c.runner.Run)
	}()
	return c, nil
}

func (c *Consumer) Ready() bool { return c != nil && c.ready.Load() }

func (c *Consumer) Status() ConsumerStatus {
	if c == nil {
		return ConsumerStatus{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return ConsumerStatus{
		Ready: c.ready.Load(), FilterSubjects: append([]string(nil), c.filters...),
		FilterRefreshError: c.filterRefreshError,
	}
}

func (c *Consumer) CurrentFilterSubjects() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.filters...)
}

func (c *Consumer) LaneStatuses() []trigger.LaneStatus {
	if c == nil || c.handler == nil {
		return nil
	}
	return c.handler.LaneStatuses()
}

func (c *Consumer) RefreshFilters(ctx context.Context) error {
	if c == nil || c.sets == nil {
		return fmt.Errorf("factor period consumer set locator is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	filters, err := c.sets.FilterSubjects(ctx)
	if err != nil {
		c.mu.Lock()
		c.filterRefreshError = err.Error()
		c.mu.Unlock()
		return err
	}
	filters = trigger.NormalizeFilters(filters)
	c.mu.Lock()
	c.filters = filters
	c.filterRefreshError = ""
	c.mu.Unlock()
	return nil
}

// SetsChanged implements catalog.Notifier. The family subscription remains
// stable while the exact set list is refreshed for status and routing state.
func (c *Consumer) SetsChanged() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.RefreshFilters(ctx); err != nil {
		log.ErrorContextf(ctx, "refresh factor period consumer filters: %v", err)
	}
}

func (c *Consumer) Close() error {
	if c == nil {
		return nil
	}
	var closeErr error
	c.closeOnce.Do(func() {
		c.ready.Store(false)
		if c.cancel != nil {
			c.cancel()
		}
		if c.consumer != nil {
			closeErr = c.consumer.Close()
		}
		c.runWG.Wait()
		if c.handler != nil {
			c.handler.Close()
		}
		if c.client != nil {
			closeErr = errors.Join(closeErr, c.client.Close())
		}
	})
	return closeErr
}

func runUntilCancelled(ctx context.Context, run func(context.Context) error) {
	for ctx.Err() == nil {
		if err := run(ctx); err != nil && ctx.Err() == nil {
			log.ErrorContextf(ctx, "factor period consumer stopped: %v", err)
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
