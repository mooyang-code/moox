package eventconsumer

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

// RowsConsumer is the engine durable for mdataset input commits.
type RowsConsumer struct {
	client   *jetstream.Client
	consumer *events.Consumer
	runner   *jetstream.Runner
	cancel   context.CancelFunc
	ready    bool
}

func StartDatasetRows(ctx context.Context, cfg Config, handler *trigger.DatasetRowsRunner, filters []string) (*RowsConsumer, error) {
	if handler == nil {
		return nil, fmt.Errorf("dataset rows handler is required")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	clientCfg := jetstream.ConfigFromEnv(append([]string(nil), cfg.URLs...), "moox-factor-engine")
	if cfg.CredentialFile != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
			return nil, err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	consumer, err := events.NewConsumer(ctx, client, registry, trigger.DatasetRowsConsumerConfig(nil, cfg.FetchMaxWait, filters))
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	ingress := &RowsConsumer{client: client, consumer: consumer, cancel: cancel, ready: true}
	ingress.runner = jetstream.NewRunner(consumer, datasetRowsHandler{runner: handler}, jetstream.RunnerConfig{
		BatchSize: 1, InProgressInterval: 30 * time.Second,
		ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			log.ErrorContextf(runCtx, "factor dataset rows consumer error: %v", err)
		}),
	})
	go func() {
		if runErr := ingress.runner.Run(runCtx); runErr != nil && runCtx.Err() == nil {
			log.ErrorContextf(runCtx, "factor dataset rows consumer stopped: %v", runErr)
		}
	}()
	return ingress, nil
}

func (c *RowsConsumer) Ready() bool {
	return c != nil && c.ready
}

func (c *RowsConsumer) Status() Status {
	return Status{}
}

func (c *RowsConsumer) Close() error {
	if c == nil {
		return nil
	}
	c.ready = false
	if c.cancel != nil {
		c.cancel()
	}
	var consumerErr error
	if c.consumer != nil {
		consumerErr = c.consumer.Close()
	}
	if c.client != nil {
		return joinClose(consumerErr, c.client.Close())
	}
	return consumerErr
}

type datasetRowsHandler struct {
	runner *trigger.DatasetRowsRunner
}

func (h datasetRowsHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	message, payload, err := events.DecodeDatasetRowsUpsertedWithContentType(
		registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType,
	)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor dataset rows event rejected: %w", err)}
	}
	if err := h.runner.HandleDatasetRows(ctx, message.GetEventId(), payload); err != nil {
		log.ErrorContextf(ctx, "factor dataset rows handle failed dataset=%s: %v", payload.GetDatasetId(), err)
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func joinClose(left, right error) error {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return fmt.Errorf("%v; %v", left, right)
}
