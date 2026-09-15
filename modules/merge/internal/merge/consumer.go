package merge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	writeKindInputCommit  = "input_commit"
	writeKindFactorPatch  = "factor_patch"
	rowConsumerName       = "merge_rows_v1"
	collectorConsumerName = "merge_collector_v1"
)

// RowHandler routes source Dataset row events onto the assemblers that own them.
type RowHandler struct {
	bySource map[string][]*Assembler
	Now      func() time.Time
}

func NewRowHandler(assemblers ...*Assembler) *RowHandler {
	handler := &RowHandler{bySource: map[string][]*Assembler{}}
	for _, assembler := range assemblers {
		if assembler == nil {
			continue
		}
		for _, sourceID := range assembler.SourceDatasetIDs() {
			handler.bySource[sourceID] = append(handler.bySource[sourceID], assembler)
		}
	}
	return handler
}

func (h *RowHandler) now() time.Time {
	if h != nil && h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

func arrivalStillLive(period time.Time, freq string, now time.Time) bool {
	interval, err := report.ParseDatasetFrequency(freq)
	if err != nil || interval <= 0 {
		interval = time.Minute
	}
	// Collector fires at the next bar (period+interval). Strategy ValidUntil is
	// period+2*interval. Apply only while the result could still be committed.
	return now.UTC().Before(period.UTC().Add(2 * interval))
}

func (h *RowHandler) HandleDatasetRows(ctx context.Context, payload *storagepb.DatasetRowsUpserted) error {
	if h == nil || payload == nil {
		return nil
	}
	switch strings.TrimSpace(payload.GetWriteKind()) {
	case writeKindFactorPatch, writeKindInputCommit:
		return nil
	}
	assemblers := h.bySource[strings.TrimSpace(payload.GetDatasetId())]
	if len(assemblers) == 0 {
		return nil
	}
	for _, row := range payload.GetRows() {
		if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
			continue
		}
		ts := row.GetKey().GetTimeSeries()
		period, err := parseRowTime(ts.GetDataTime())
		if err != nil {
			return err
		}
		if !arrivalStillLive(period, ts.GetFreq(), h.now()) {
			continue
		}
		fields := numericFields(row.GetFields())
		for _, assembler := range assemblers {
			key := assembler.rowKey(ts.GetSubjectId(), ts.GetFreq(), ts.GetSeriesTag(), period)
			if err := assembler.ApplyArrival(ctx, key, payload.GetDatasetId(), fields); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *RowHandler) HandleCollectorCompleted(ctx context.Context, payload *storagepb.CollectorPeriodCompleted) error {
	if h == nil || payload == nil {
		return nil
	}
	assemblers := h.bySource[strings.TrimSpace(payload.GetDatasetId())]
	if len(assemblers) == 0 {
		return nil
	}
	period := time.Unix(payload.GetPeriodTime(), 0).UTC()
	for _, assembler := range assemblers {
		if err := assembler.NoteCollectorCompleted(ctx, payload.GetDatasetId(), period, payload.GetUniverseSubjectIds()); err != nil {
			return err
		}
	}
	return nil
}

func (h *RowHandler) HandleCollectorDelivery(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	_, payload, err := events.DecodeCollectorPeriodCompletedWithContentType(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("merge collector event rejected: %w", err)}
	}
	if err := h.HandleCollectorCompleted(ctx, payload); err != nil {
		log.ErrorContextf(ctx, "merge collector complete failed dataset=%s: %v", payload.GetDatasetId(), err)
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func (h *RowHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	_, payload, err := events.DecodeDatasetRowsUpsertedWithContentType(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("merge row event rejected: %w", err)}
	}
	if err := h.HandleDatasetRows(ctx, payload); err != nil {
		log.ErrorContextf(ctx, "merge apply arrival failed dataset=%s: %v", payload.GetDatasetId(), err)
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

type RowConsumer struct {
	client   *jetstream.Client
	consumer *events.Consumer
	runner   *jetstream.Runner
	cancel   context.CancelFunc
}

func StartRowConsumer(ctx context.Context, cfg ProcessConfig, handler *RowHandler) (*RowConsumer, error) {
	if handler == nil {
		return nil, fmt.Errorf("merge row handler is required")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	filters, err := sourceFilters(registry, cfg)
	if err != nil {
		return nil, err
	}
	clientCfg := jetstream.ConfigFromEnv(append([]string(nil), cfg.EventBus.URLs...), "moox-merge")
	if strings.TrimSpace(cfg.EventBus.CredentialFile) != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.EventBus.CredentialFile)); err != nil {
			return nil, err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	consumer, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
		Name: rowConsumerName, Stream: events.DatasetRowsUpserted.Stream(), FilterSubjects: filters,
		AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 16, FetchMaxWait: cfg.EventBus.FetchMaxWait,
		DeliverPolicy: nats.DeliverNewPolicy, DeliverDecodeErrors: true,
	})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	runner := jetstream.NewRunner(consumer, handler, jetstream.RunnerConfig{
		BatchSize: 1, InProgressInterval: 30 * time.Second,
		ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			log.ErrorContextf(runCtx, "merge row consumer error: %v", err)
		}),
	})
	go func() {
		_ = runUntilCancelled(runCtx, time.Second, runner.Run, func(err error) {
			log.ErrorContextf(runCtx, "merge row consumer stopped: %v", err)
		})
	}()
	return &RowConsumer{client: client, consumer: consumer, runner: runner, cancel: cancel}, nil
}

type collectorDeliveryHandler struct {
	inner *RowHandler
}

func (h collectorDeliveryHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	return h.inner.HandleCollectorDelivery(ctx, delivery)
}

func StartCollectorConsumer(ctx context.Context, cfg ProcessConfig, handler *RowHandler) (*RowConsumer, error) {
	if handler == nil {
		return nil, fmt.Errorf("merge row handler is required")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	filters, err := collectorFilters(registry, cfg)
	if err != nil {
		return nil, err
	}
	clientCfg := jetstream.ConfigFromEnv(append([]string(nil), cfg.EventBus.URLs...), "moox-merge")
	if strings.TrimSpace(cfg.EventBus.CredentialFile) != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.EventBus.CredentialFile)); err != nil {
			return nil, err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	consumer, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
		Name: collectorConsumerName, Stream: events.CollectorPeriodCompleted.Stream(), FilterSubjects: filters,
		AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 16, FetchMaxWait: cfg.EventBus.FetchMaxWait,
		DeliverPolicy: nats.DeliverNewPolicy, DeliverDecodeErrors: true,
	})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	runner := jetstream.NewRunner(consumer, collectorDeliveryHandler{inner: handler}, jetstream.RunnerConfig{
		BatchSize: 1, InProgressInterval: 30 * time.Second,
		ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			log.ErrorContextf(runCtx, "merge collector consumer error: %v", err)
		}),
	})
	go func() {
		_ = runUntilCancelled(runCtx, time.Second, runner.Run, func(err error) {
			log.ErrorContextf(runCtx, "merge collector consumer stopped: %v", err)
		})
	}()
	return &RowConsumer{client: client, consumer: consumer, runner: runner, cancel: cancel}, nil
}

func runUntilCancelled(ctx context.Context, delay time.Duration, run func(context.Context) error, onErr func(error)) error {
	if delay <= 0 {
		delay = time.Second
	}
	for ctx.Err() == nil {
		err := run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && onErr != nil {
			onErr(err)
		}
		if !sleepConsumer(ctx, delay) {
			return nil
		}
	}
	return nil
}

func sleepConsumer(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *RowConsumer) Close() error {
	if c == nil {
		return nil
	}
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

func sourceFilters(registry *events.Registry, cfg ProcessConfig) ([]string, error) {
	seen := map[string]struct{}{}
	var filters []string
	for _, def := range cfg.Definitions {
		for _, source := range def.Sources {
			subject, err := registry.RenderSubject(events.DatasetRowsUpserted, def.SpaceID, source.DatasetID)
			if err != nil {
				return nil, err
			}
			if _, ok := seen[subject]; ok {
				continue
			}
			seen[subject] = struct{}{}
			filters = append(filters, subject)
		}
	}
	if len(filters) == 0 {
		return nil, fmt.Errorf("merge source dataset filters are required")
	}
	return filters, nil
}

func collectorFilters(registry *events.Registry, cfg ProcessConfig) ([]string, error) {
	seen := map[string]struct{}{}
	var filters []string
	for _, def := range cfg.Definitions {
		for _, source := range def.Sources {
			subject, err := registry.RenderSubject(events.CollectorPeriodCompleted, def.SpaceID, source.DatasetID)
			if err != nil {
				return nil, err
			}
			if _, ok := seen[subject]; ok {
				continue
			}
			seen[subject] = struct{}{}
			filters = append(filters, subject)
		}
	}
	if len(filters) == 0 {
		return nil, fmt.Errorf("merge collector dataset filters are required")
	}
	return filters, nil
}

func numericFields(fields []*storagepb.FieldValue) map[string]float64 {
	out := make(map[string]float64, len(fields))
	for _, field := range fields {
		if field == nil || strings.TrimSpace(field.GetFieldId()) == "" || field.GetValue() == nil {
			continue
		}
		switch typed := field.GetValue().GetValue().(type) {
		case *storagepb.TypedValue_DoubleValue:
			out[field.GetFieldId()] = typed.DoubleValue
		case *storagepb.TypedValue_IntValue:
			out[field.GetFieldId()] = float64(typed.IntValue)
		}
	}
	return out
}

func parseRowTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("row data_time is required")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse row data_time %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

func joinClose(first, second error) error {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	return fmt.Errorf("%v; %w", first, second)
}
