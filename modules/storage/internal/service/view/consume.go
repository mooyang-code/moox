package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/eventcontract"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

// EventConsumerOptions controls the client-side dispatch policy. MaxAckPending
// is intentionally not sent to JetStream: the eventbus topology owns it and
// this value is only useful for validating a local fetch configuration.
type EventConsumerOptions struct {
	Stream        string
	Consumer      string
	AckWaitMS     int
	FetchBatch    int
	MaxWorkers    int
	MaxAckPending int
	Ordering      string
	// MaxRetryAttempts bounds client-side retries for transient projection
	// failures. A value of zero uses the safe default; the broker topology is
	// still authoritative for the durable's immutable settings.
	MaxRetryAttempts int
	ErrorReporter    jetstream.ErrorReporter
	Metrics          *observability.ViewMetrics
	// BeforeProcess is an optional test/diagnostic hook. Production callers
	// leave it nil; it runs inside the subject lane before projection work.
	BeforeProcess func(context.Context, *jetstream.Delivery) error
}

const (
	defaultMaxRetryAttempts = 10
)

func (o EventConsumerOptions) withDefaults() (EventConsumerOptions, error) {
	if strings.TrimSpace(o.Stream) == "" {
		o.Stream = "MOOX_STORAGE"
	}
	if strings.TrimSpace(o.Consumer) == "" {
		o.Consumer = "storage_view"
	}
	if o.AckWaitMS == 0 {
		o.AckWaitMS = 120000
	}
	if o.FetchBatch == 0 {
		o.FetchBatch = 8
	}
	if o.MaxAckPending == 0 {
		o.MaxAckPending = o.FetchBatch
	}
	if o.MaxWorkers == 0 {
		o.MaxWorkers = 4
	}
	if o.MaxRetryAttempts == 0 {
		// Zero means "use the safe default"; negative values remain invalid
		// and are rejected below instead of being silently corrected.
		o.MaxRetryAttempts = defaultMaxRetryAttempts
	}
	o.Stream = strings.TrimSpace(o.Stream)
	o.Consumer = strings.TrimSpace(o.Consumer)
	if strings.TrimSpace(o.Ordering) == "" {
		o.Ordering = "subject"
	}
	o.Ordering = strings.ToLower(strings.TrimSpace(o.Ordering))
	if o.FetchBatch < 1 {
		return o, errors.New("storage view fetch_batch must be positive")
	}
	if o.MaxWorkers < 1 {
		return o, errors.New("storage view max_workers must be positive")
	}
	if o.MaxRetryAttempts < 1 {
		return o, errors.New("storage view max_retry_attempts must be positive")
	}
	if o.Ordering != "subject" {
		return o, fmt.Errorf("storage view ordering %q is unsupported", o.Ordering)
	}
	if o.MaxAckPending < 0 {
		return o, errors.New("storage view max_ack_pending must not be negative")
	}
	if o.AckWaitMS < 1 {
		return o, errors.New("storage view ack_wait_ms must be positive")
	}
	if o.MaxAckPending > 0 && o.FetchBatch > o.MaxAckPending {
		return o, fmt.Errorf("storage view fetch_batch %d exceeds max_ack_pending %d", o.FetchBatch, o.MaxAckPending)
	}
	return o, nil
}

func (s *Service) StartEventConsumer(ctx context.Context, client *jetstream.Client, configured ...EventConsumerOptions) (func(), error) {
	if s == nil {
		return nil, errors.New("storage view service is nil")
	}
	if client == nil {
		return nil, errors.New("eventbus client is required")
	}
	if ctx == nil {
		return nil, errors.New("storage view consumer context is required")
	}
	opts := EventConsumerOptions{}
	if len(configured) > 0 {
		opts = configured[0]
	}
	var err error
	if opts, err = opts.withDefaults(); err != nil {
		return nil, err
	}
	if opts.Metrics == nil {
		opts.Metrics = s.metrics
	}
	if opts.Metrics == nil {
		opts.Metrics = observability.DefaultViewMetrics
	}
	s.metrics = opts.Metrics
	reporter := opts.ErrorReporter
	if reporter == nil {
		reporter = jetstream.ErrorReporterFunc(func(err error) {
			log.Printf("storage view event consumer error: %v", err)
		})
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	consumer, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
		Name: opts.Consumer, Event: events.DatasetRowsUpserted,
		AckWait:    time.Duration(opts.AckWaitMS) * time.Millisecond,
		MaxDeliver: -1, MaxAckPending: opts.MaxAckPending,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		return nil, err
	}
	opts.Metrics.SetConsumerBound(true)
	loopCtx, cancel := context.WithCancel(ctx)
	dispatcher := newSubjectLaneDispatcher(loopCtx, opts.MaxWorkers, func(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat) error {
		if opts.BeforeProcess != nil {
			if err := opts.BeforeProcess(ctx, delivery); err != nil {
				return err
			}
		}
		return s.processDeliveryWithPolicy(ctx, delivery, heartbeat, opts.MaxRetryAttempts)
	}, reporter, laneMetricsHooks{
		newHeartbeat: func(ctx context.Context, delivery *jetstream.Delivery) *deliveryHeartbeat {
			return newDeliveryHeartbeat(ctx, delivery, deliveryHeartbeatInterval(time.Duration(opts.AckWaitMS)*time.Millisecond), opts.Metrics)
		},
		onSubmit: func(delivery *jetstream.Delivery) {
			opts.Metrics.ObserveLaneSubmit()
			opts.Metrics.ObservePendingDelivery(delivery, time.Now().UTC())
		},
		onStart: func(*jetstream.Delivery) { opts.Metrics.IncLaneActive() },
		onFinish: func(delivery *jetstream.Delivery) {
			opts.Metrics.DecLaneActive()
			opts.Metrics.AddConsumerLagMessages(-1)
			opts.Metrics.CompletePendingDelivery(delivery, time.Now().UTC())
		},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer opts.Metrics.SetConsumerBound(false)
		defer consumer.Close()
		defer dispatcher.Close()
		for loopCtx.Err() == nil {
			deliveries, fetchErr := consumer.Fetch(loopCtx, opts.FetchBatch)
			opts.Metrics.AddConsumerLagMessages(int64(len(deliveries)))
			for _, delivery := range deliveries {
				opts.Metrics.ObservePendingDelivery(delivery, time.Now().UTC())
				if err := dispatcher.Dispatch(delivery); err != nil {
					opts.Metrics.AddConsumerLagMessages(-1)
					opts.Metrics.CompletePendingDelivery(delivery, time.Now().UTC())
					if loopCtx.Err() == nil {
						reporter.Report(fmt.Errorf("dispatch storage view delivery: %w", err))
					}
				}
			}
			if fetchErr != nil {
				if loopCtx.Err() != nil {
					return
				}
				if !errors.Is(fetchErr, nats.ErrTimeout) {
					opts.Metrics.SetConsumerBound(false)
					reporter.Report(fmt.Errorf("fetch storage view deliveries: %w", fetchErr))
				}
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-timer.C:
				case <-loopCtx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				}
				continue
			}
			opts.Metrics.SetConsumerBound(true)
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (s *Service) processDelivery(ctx context.Context, delivery *jetstream.Delivery, queued ...*deliveryHeartbeat) error {
	return s.processDeliveryWithPolicy(ctx, delivery, firstHeartbeat(queued), defaultMaxRetryAttempts)
}

func firstHeartbeat(queued []*deliveryHeartbeat) *deliveryHeartbeat {
	if len(queued) == 0 {
		return nil
	}
	return queued[0]
}

func (s *Service) processDeliveryWithPolicy(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int) error {
	return s.processDeliveryWithApply(ctx, delivery, heartbeat, maxRetryAttempts, func(ctx context.Context, delivery *jetstream.Delivery) error {
		return s.applyDelivery(ctx, delivery)
	})
}

func (s *Service) processDeliveryWithApply(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int, apply func(context.Context, *jetstream.Delivery) error) error {
	return s.processDeliveryWithApplyAndActions(ctx, delivery, heartbeat, maxRetryAttempts, apply, deliveryActions{
		ack:      delivery.Ack,
		progress: delivery.InProgress,
		term:     delivery.Term,
	})
}

type deliveryActions struct {
	ack      func(context.Context) error
	progress func(context.Context) error
	term     func(context.Context) error
}

func (s *Service) processDeliveryWithApplyAndActions(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat, maxRetryAttempts int, apply func(context.Context, *jetstream.Delivery) error, actions deliveryActions) error {
	if s == nil {
		return errors.New("storage view service is nil")
	}
	if delivery == nil {
		return errors.New("storage view delivery is nil")
	}
	if apply == nil || actions.ack == nil || actions.progress == nil || actions.term == nil {
		return errors.New("storage view delivery policy is incomplete")
	}
	if ctx == nil {
		return errors.New("storage view delivery context is required")
	}
	started := time.Now()
	metrics := s.metrics
	if metrics == nil {
		metrics = observability.DefaultViewMetrics
	}
	defer func() { metrics.ObserveDeliveryDuration(time.Since(started)) }()
	if delivery != nil && delivery.DeliveryCount > 1 {
		metrics.IncRedelivery()
	}
	s.liveWork.Add(1)
	defer s.liveWork.Add(-1)
	if maxRetryAttempts < 1 {
		maxRetryAttempts = defaultMaxRetryAttempts
	}
	if heartbeat == nil {
		heartbeat = newDeliveryHeartbeat(ctx, delivery, deliveryHeartbeatInterval(120*time.Second), metrics)
	}
	defer func() { heartbeat.stop() }()
	if err := s.acquireLiveDelivery(ctx, delivery); err != nil {
		return errors.Join(err, heartbeat.err())
	}
	defer s.releaseLiveDelivery()
	retryCount := 0
	for ctx.Err() == nil {
		err := apply(ctx, delivery)
		if err == nil {
			// Applying a projection is deliberately separate from ACK retry:
			// an ACK transport failure must never repeat an already successful
			// index write.
			for ctx.Err() == nil {
				if ackErr := actions.ack(ctx); ackErr == nil {
					metrics.ObserveDelivery("ack", "success")
					return heartbeat.err()
				} else {
					log.Printf("storage view delivery ack failed: %v", ackErr)
					metrics.IncAckError()
					metrics.ObserveDelivery("ack", "error")
					heartbeat.report(ackErr)
				}
				if !sleepDeliveryRetry(ctx, time.Second) {
					return ctx.Err()
				}
			}
			return ctx.Err()
		}
		if isPermanentDeliveryError(err) {
			for ctx.Err() == nil {
				if termErr := actions.term(ctx); termErr == nil {
					metrics.ObserveDelivery("term", "success")
					return heartbeat.err()
				} else {
					log.Printf("storage view delivery term failed after permanent error %v: %v", err, termErr)
					metrics.IncAckError()
					metrics.ObserveDelivery("term", "error")
					heartbeat.report(termErr)
					if errors.Is(termErr, jetstream.ErrInvalidDelivery) || errors.Is(termErr, jetstream.ErrClosed) {
						return errors.Join(err, termErr, heartbeat.err())
					}
				}
				if !sleepDeliveryRetry(ctx, time.Second) {
					return ctx.Err()
				}
			}
			return ctx.Err()
		}
		retryCount++
		if retryCount >= maxRetryAttempts {
			metrics.IncRetryExhausted()
			log.Printf("storage view delivery retry exhausted: consumer=%s event_id=%s subject=%s delivery_count=%d decision=TERM reason=%v",
				delivery.Consumer, delivery.RawMessageID, delivery.Subject, delivery.DeliveryCount, err)
			if termErr := actions.term(ctx); termErr != nil {
				metrics.IncAckError()
				metrics.ObserveDelivery("term", "error")
				return errors.Join(err, termErr, heartbeat.err())
			}
			metrics.ObserveDelivery("term", "success")
			return errors.Join(err, heartbeat.err())
		}
		// Keep the delivery pending while retrying. NAK would release
		// MaxAckPending and allow a later event to overtake it.
		if progressErr := actions.progress(ctx); progressErr != nil {
			log.Printf("storage view delivery progress failed after %v: %v", err, progressErr)
			metrics.IncInProgressError()
			metrics.ObserveDelivery("in_progress", "error")
			heartbeat.report(progressErr)
		} else {
			metrics.ObserveDelivery("in_progress", "success")
		}
		if !sleepDeliveryRetry(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func sleepDeliveryRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) initLiveGate() {
	s.liveGateOnce.Do(func() {
		s.liveGate = newLiveLeaseGate()
	})
}

func (s *Service) acquireBackfill(ctx context.Context) error {
	s.initLiveGate()
	return s.liveGate.acquireWrite(ctx)
}

func (s *Service) acquireLiveDelivery(ctx context.Context, delivery *jetstream.Delivery) error {
	s.initLiveGate()
	return s.liveGate.acquireRead(ctx, delivery)
}

func (s *Service) releaseLiveDelivery() {
	s.initLiveGate()
	s.liveGate.releaseRead()
}

func (s *Service) releaseBackfill() {
	s.initLiveGate()
	s.liveGate.releaseWrite()
}

// releaseLiveGate remains as a compatibility shim for old live callers.
func (s *Service) releaseLiveGate() {
	s.releaseLiveDelivery()
}

func (s *Service) applyDelivery(ctx context.Context, delivery *jetstream.Delivery) error {
	if delivery == nil {
		return permanentDeliveryError{errors.New("storage event delivery is empty")}
	}
	if delivery.DecodeError != nil {
		return permanentDeliveryError{delivery.DecodeError}
	}
	if delivery.ContentType == events.ContentType {
		registry, err := events.DefaultRegistry()
		if err != nil {
			return permanentDeliveryError{err}
		}
		event, payload, err := events.DecodeDatasetRowsUpsertedWithContentType(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
		if err != nil {
			return permanentDeliveryError{err}
		}
		rowEvent, err := eventcontract.ToLocalRows(payload)
		if err != nil {
			return permanentDeliveryError{err}
		}
		return s.applyDatasetEvent(ctx, event.GetSpaceId(), event.GetSubjectId(), rowEvent.GetRows())
	}
	return permanentDeliveryError{fmt.Errorf("unexpected storage event content type %q", delivery.ContentType)}
}

func (s *Service) applyDatasetEvent(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) error {
	s.mu.RLock()
	ref := datasetRef{spaceID: spaceID, datasetID: datasetID}
	viewKeys := make(map[viewRef]struct{})
	var standalone []string
	for id := range s.byData[ref] {
		if viewKey, ok := s.indexView[id]; ok {
			viewKeys[viewKey] = struct{}{}
		} else {
			standalone = append(standalone, id)
		}
	}
	s.mu.RUnlock()
	for viewKey := range viewKeys {
		s.mu.RLock()
		runtime := s.views[viewKey]
		s.mu.RUnlock()
		if runtime == nil {
			continue
		}
		runtime.mu.Lock()
		if err := s.applyEventToIndex(ctx, runtime.active, datasetID, rows); err != nil {
			log.Printf("storage view active index write failed space=%s view=%s index=%s dataset=%s: %v", viewKey.spaceID, viewKey.viewID, runtime.active, datasetID, err)
			runtime.mu.Unlock()
			return err
		}
		if runtime.next != "" {
			if err := s.applyEventToIndex(ctx, runtime.next, datasetID, rows); err != nil {
				failedID := runtime.next
				s.failRuntimeBuild(ctx, viewKey, runtime, err)
				runtime.next = ""
				runtime.status = "failed"
				runtime.mu.Unlock()
				s.removeFailedBuild(ctx, failedID)
				continue
			}
		}
		runtime.mu.Unlock()
	}
	for _, id := range standalone {
		if err := s.applyEventToIndex(ctx, id, datasetID, rows); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyEventToIndex(ctx context.Context, id, datasetID string, rows []*pb.RowFieldUpsert) error {
	if id == "" {
		return nil
	}
	engine, err := s.engineFor(id)
	if err != nil {
		return err
	}
	s.mu.RLock()
	schema := s.schemas[id]
	s.mu.RUnlock()
	writes := eventWrites(schema, datasetID, rows)
	if len(writes) == 0 {
		return nil
	}
	needsRecovery := datasetID != schema.PrimaryDatasetID
	if !needsRecovery {
		needsRecovery, err = s.hasMissingRows(ctx, engine, id, writes)
		if err != nil {
			return err
		}
	}
	if needsRecovery {
		writes, err = s.recoverMissingRows(ctx, engine, id, schema, datasetID, rows, writes)
		if err != nil {
			return err
		}
		if len(writes) == 0 {
			return nil
		}
	}
	mode := viewindex.LiveWrite
	if needsRecovery {
		mode = viewindex.Replace
	}
	return engine.Write(ctx, id, viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: mode})
}

func (s *Service) hasMissingRows(ctx context.Context, engine viewindex.Engine, id string, writes []viewindex.RowWrite) (bool, error) {
	keys := make([]*pb.RowKey, 0, len(writes))
	for _, write := range writes {
		keys = append(keys, write.Key.Key)
	}
	rows, _, err := engine.Query(ctx, id, viewindex.QuerySpec{Keys: keys, TotalMode: pb.TotalMode_NONE})
	if err != nil {
		return false, err
	}
	present := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row != nil && row.GetKey() != nil {
			present[viewindex.RowKeyID(row.GetKey())] = struct{}{}
		}
	}
	for _, key := range keys {
		if _, ok := present[viewindex.RowKeyID(key)]; !ok {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) recoverMissingRows(ctx context.Context, _ viewindex.Engine, _ string, schema viewindex.ViewIndexSchema, datasetID string, events []*pb.RowFieldUpsert, writes []viewindex.RowWrite) ([]viewindex.RowWrite, error) {
	s.mu.RLock()
	reader := s.primary
	auth := s.primaryAuth
	if auth != nil {
		auth = proto.Clone(auth).(*pb.AuthInfo)
	}
	s.mu.RUnlock()
	if reader == nil || auth == nil {
		return nil, errors.New("primary reader and auth are required to recover a missing view row")
	}
	type sourceField struct{ dataset, source, target string }
	var sources []sourceField
	byDataset := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, column := range schema.Columns {
		if column == nil {
			continue
		}
		sourceDataset := viewColumnDataset(column)
		origin := column.GetOriginId()
		if sourceDataset == "" || origin == "" {
			continue
		}
		if index := strings.LastIndexByte(origin, '.'); index >= 0 && index+1 < len(origin) {
			origin = origin[index+1:]
		}
		sources = append(sources, sourceField{dataset: sourceDataset, source: origin, target: column.GetColumnName()})
		if seen[sourceDataset] == nil {
			seen[sourceDataset] = make(map[string]struct{})
		}
		if _, ok := seen[sourceDataset][origin]; !ok {
			seen[sourceDataset][origin] = struct{}{}
			byDataset[sourceDataset] = append(byDataset[sourceDataset], origin)
		}
	}
	eventRows := make(map[string]*pb.RowFieldUpsert, len(events))
	for _, event := range events {
		if event == nil || event.GetKey() == nil {
			continue
		}
		key := proto.Clone(event.GetKey()).(*pb.RowKey)
		key.DatasetId = schema.PrimaryDatasetID
		eventRows[viewindex.RowKeyID(key)] = event
	}
	type sourceRows struct {
		values  map[string]*pb.RowFieldValues
		present map[string]struct{}
	}
	rowsByDataset := make(map[string]sourceRows)
	for sourceDataset, fieldIDs := range byDataset {
		keys := make([]*pb.RowKey, 0, len(writes))
		for _, write := range writes {
			key := proto.Clone(write.Key.Key).(*pb.RowKey)
			key.DatasetId = sourceDataset
			keys = append(keys, key)
		}
		rsp, err := reader.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: auth, Keys: keys, FieldIds: fieldIDs})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		loaded := sourceRows{values: make(map[string]*pb.RowFieldValues), present: make(map[string]struct{})}
		for _, row := range rsp.GetRows() {
			if row != nil && row.GetKey() != nil && (len(row.GetFields()) != 0 || len(row.GetAttributes()) != 0) {
				loaded.values[viewindex.RowKeyID(row.GetKey())] = row
				loaded.present[viewindex.RowKeyID(row.GetKey())] = struct{}{}
			}
		}
		for _, key := range rsp.GetExistingKeys() {
			if key != nil {
				loaded.present[viewindex.RowKeyID(key)] = struct{}{}
			}
		}
		rowsByDataset[sourceDataset] = loaded
	}
	result := make([]viewindex.RowWrite, 0, len(writes))
	for _, write := range writes {
		primaryKey := proto.Clone(write.Key.Key).(*pb.RowKey)
		primaryKey.DatasetId = schema.PrimaryDatasetID
		primaryID := viewindex.RowKeyID(primaryKey)
		if _, ok := rowsByDataset[schema.PrimaryDatasetID].present[primaryID]; !ok {
			continue
		}
		complete := viewindex.RowWrite{Key: write.Key}
		for _, source := range sources {
			var fields []*pb.FieldValue
			if row := rowsByDataset[source.dataset].values[viewindex.RowKeyID(withDataset(write.Key.Key, source.dataset))]; row != nil {
				fields = append(fields, row.GetFields()...)
			}
			if source.dataset == datasetID {
				if event := eventRows[primaryID]; event != nil {
					fields = append(fields, event.GetFields()...)
				}
			}
			complete.Fields = appendMatchingField(complete.Fields, fields, source.source, source.target)
		}
		result = append(result, complete)
	}
	return result, nil
}

func withDataset(key *pb.RowKey, datasetID string) *pb.RowKey {
	clone := proto.Clone(key).(*pb.RowKey)
	clone.DatasetId = datasetID
	return clone
}

func appendMatchingField(dst, fields []*pb.FieldValue, source, target string) []*pb.FieldValue {
	for _, field := range fields {
		if field != nil && field.GetFieldId() == source {
			value := &pb.FieldValue{FieldId: target, Value: field.GetValue()}
			for index, existing := range dst {
				if existing != nil && existing.GetFieldId() == target {
					dst[index] = value
					value = nil
					break
				}
			}
			if value != nil {
				dst = append(dst, value)
			}
		}
	}
	return dst
}

func eventWrites(schema viewindex.ViewIndexSchema, datasetID string, rows []*pb.RowFieldUpsert) []viewindex.RowWrite {
	columns := make(map[string]string)
	for _, column := range schema.Columns {
		if column == nil || viewColumnDataset(column) != datasetID {
			continue
		}
		source := viewColumnSource(column, datasetID)
		if source != "" {
			columns[source] = column.GetColumnName()
		}
	}
	if len(columns) == 0 {
		return nil
	}
	writes := make([]viewindex.RowWrite, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		fields := make([]*pb.FieldValue, 0, len(row.GetFields()))
		for _, field := range row.GetFields() {
			if name := columns[field.GetFieldId()]; name != "" {
				fields = append(fields, &pb.FieldValue{FieldId: name, Value: field.GetValue()})
			}
		}
		if len(fields) != 0 {
			key := proto.Clone(row.GetKey()).(*pb.RowKey)
			if schema.PrimaryDatasetID != "" {
				key.DatasetId = schema.PrimaryDatasetID
			}
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: key}, Fields: fields})
		}
	}
	return writes
}

func viewColumnSource(column *pb.ViewColumn, datasetID string) string {
	if column == nil {
		return ""
	}
	origin := column.GetOriginId()
	prefix := datasetID + "."
	if strings.HasPrefix(origin, prefix) {
		return strings.TrimPrefix(origin, prefix)
	}
	if idx := strings.LastIndexByte(origin, '.'); idx >= 0 && idx+1 < len(origin) {
		return origin[idx+1:]
	}
	return ""
}

func viewColumnDataset(column *pb.ViewColumn) string {
	if column == nil {
		return ""
	}
	origin := column.GetOriginId()
	if idx := strings.LastIndexByte(origin, '.'); idx > 0 {
		return origin[:idx]
	}
	return ""
}

type permanentDeliveryError struct{ error }

func isPermanentDeliveryError(err error) bool {
	var target permanentDeliveryError
	return errors.As(err, &target)
}

type laneHandler func(context.Context, *jetstream.Delivery, *deliveryHeartbeat) error

type laneMetricsHooks struct {
	newHeartbeat func(context.Context, *jetstream.Delivery) *deliveryHeartbeat
	onSubmit     func(*jetstream.Delivery)
	onStart      func(*jetstream.Delivery)
	onFinish     func(*jetstream.Delivery)
}

type subjectLane struct {
	subject string
	queue   []*laneDelivery
	running bool
}

type laneDelivery struct {
	delivery  *jetstream.Delivery
	heartbeat *deliveryHeartbeat
}

// subjectLaneDispatcher is a scheduler rather than a plain worker pool: a
// lane is queued at most once, so one subject cannot have two active handlers.
type subjectLaneDispatcher struct {
	ctx        context.Context
	cancel     context.CancelFunc
	maxWorkers int
	handler    laneHandler
	reporter   jetstream.ErrorReporter
	hooks      laneMetricsHooks
	ready      chan *subjectLane
	lanes      map[string]*subjectLane
	mu         sync.Mutex
	closed     bool
	wg         sync.WaitGroup
}

func newSubjectLaneDispatcher(parent context.Context, maxWorkers int, handler laneHandler, reporter jetstream.ErrorReporter, hooks ...laneMetricsHooks) *subjectLaneDispatcher {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	ctx, cancel := context.WithCancel(parent)
	var metricsHooks laneMetricsHooks
	if len(hooks) > 0 {
		metricsHooks = hooks[0]
	}
	d := &subjectLaneDispatcher{
		ctx: ctx, cancel: cancel, maxWorkers: maxWorkers, handler: handler, reporter: reporter,
		hooks: metricsHooks,
		ready: make(chan *subjectLane, maxWorkers), lanes: make(map[string]*subjectLane),
	}
	d.wg.Add(maxWorkers)
	for i := 0; i < maxWorkers; i++ {
		go d.worker()
	}
	return d
}

func (d *subjectLaneDispatcher) Dispatch(delivery *jetstream.Delivery) error {
	if d == nil || delivery == nil {
		return errors.New("storage view lane delivery is nil")
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.New("storage view lane dispatcher is closed")
	}
	lane := d.lanes[delivery.Subject]
	if lane == nil {
		lane = &subjectLane{subject: delivery.Subject}
		d.lanes[delivery.Subject] = lane
	}
	var heartbeat *deliveryHeartbeat
	if d.hooks.newHeartbeat != nil {
		heartbeat = d.hooks.newHeartbeat(d.ctx, delivery)
	}
	lane.queue = append(lane.queue, &laneDelivery{delivery: delivery, heartbeat: heartbeat})
	start := !lane.running
	if start {
		lane.running = true
	}
	d.mu.Unlock()
	if d.hooks.onSubmit != nil {
		d.hooks.onSubmit(delivery)
	}
	if start {
		select {
		case d.ready <- lane:
		case <-d.ctx.Done():
			return d.ctx.Err()
		}
	}
	return nil
}

func (d *subjectLaneDispatcher) worker() {
	defer d.wg.Done()
	for {
		select {
		case lane := <-d.ready:
			if lane == nil {
				continue
			}
			item, ok := d.next(lane)
			if !ok {
				continue
			}
			delivery := item.delivery
			if d.hooks.onStart != nil {
				d.hooks.onStart(delivery)
			}
			if d.handler != nil {
				if err := d.handler(d.ctx, delivery, item.heartbeat); err != nil && d.ctx.Err() == nil && d.reporter != nil {
					d.reporter.Report(fmt.Errorf("storage view subject %q delivery failed: %w", lane.subject, err))
				}
			}
			if item.heartbeat != nil {
				item.heartbeat.stop()
			}
			if d.hooks.onFinish != nil {
				d.hooks.onFinish(delivery)
			}
			d.finish(lane)
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *subjectLaneDispatcher) next(lane *subjectLane) (*laneDelivery, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(lane.queue) == 0 {
		lane.running = false
		delete(d.lanes, lane.subject)
		return nil, false
	}
	delivery := lane.queue[0]
	lane.queue = lane.queue[1:]
	return delivery, true
}

func (d *subjectLaneDispatcher) finish(lane *subjectLane) {
	d.mu.Lock()
	if len(lane.queue) == 0 {
		lane.running = false
		delete(d.lanes, lane.subject)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	select {
	case d.ready <- lane:
	default:
		// Never let all workers block trying to requeue while ready already
		// contains other lanes. The helper exits with the dispatcher.
		go d.enqueue(lane)
	}
}

func (d *subjectLaneDispatcher) enqueue(lane *subjectLane) {
	select {
	case d.ready <- lane:
	case <-d.ctx.Done():
	}
}

func (d *subjectLaneDispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	var queued []*deliveryHeartbeat
	if !d.closed {
		d.closed = true
		d.cancel()
		for _, lane := range d.lanes {
			for _, item := range lane.queue {
				if item != nil && item.heartbeat != nil {
					queued = append(queued, item.heartbeat)
				}
			}
			lane.queue = nil
		}
	}
	d.mu.Unlock()
	for _, heartbeat := range queued {
		heartbeat.stop()
	}
	d.wg.Wait()
}

type deliveryHeartbeat struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	mu       sync.Mutex
	errs     []error
	metrics  *observability.ViewMetrics
}

func newDeliveryHeartbeat(ctx context.Context, delivery *jetstream.Delivery, interval time.Duration, metrics ...*observability.ViewMetrics) *deliveryHeartbeat {
	var metricSink *observability.ViewMetrics
	if len(metrics) > 0 {
		metricSink = metrics[0]
	}
	h := &deliveryHeartbeat{stopCh: make(chan struct{}), doneCh: make(chan struct{}), metrics: metricSink}
	if ctx == nil || delivery == nil || interval <= 0 {
		close(h.doneCh)
		return h
	}
	go func() {
		defer close(h.doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				if err := delivery.InProgress(ctx); err != nil {
					log.Printf("storage view delivery in-progress failed: %v", err)
					if h.metrics != nil {
						h.metrics.IncInProgressError()
						h.metrics.ObserveDelivery("in_progress", "error")
					}
					h.report(fmt.Errorf("storage view delivery in-progress: %w", err))
				} else {
					if h.metrics != nil {
						h.metrics.ObserveDelivery("in_progress", "success")
					}
				}
			case <-h.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return h
}

func deliveryHeartbeatInterval(ackWait time.Duration) time.Duration {
	if ackWait <= 0 {
		ackWait = 120 * time.Second
	}
	interval := ackWait / 3
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

func (h *deliveryHeartbeat) report(err error) {
	if h == nil || err == nil {
		return
	}
	h.mu.Lock()
	h.errs = append(h.errs, err)
	h.mu.Unlock()
}

func (h *deliveryHeartbeat) err() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return errors.Join(h.errs...)
}

func (h *deliveryHeartbeat) stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stopCh) })
	<-h.doneCh
}

// liveLeaseGate gives backfill writer priority. Once a writer is waiting,
// new real-time reads stop entering and the writer waits for readers to drain.
type liveLeaseGate struct {
	mu             sync.Mutex
	readers        int
	writer         bool
	waitingWriters int
	notify         chan struct{}
}

func newLiveLeaseGate() *liveLeaseGate {
	return &liveLeaseGate{notify: make(chan struct{})}
}

func (g *liveLeaseGate) acquireRead(ctx context.Context, _ *jetstream.Delivery) error {
	if ctx == nil {
		return errors.New("storage view read lease context is required")
	}
	for {
		g.mu.Lock()
		if !g.writer && g.waitingWriters == 0 {
			g.readers++
			g.mu.Unlock()
			return nil
		}
		notify := g.notify
		g.mu.Unlock()
		select {
		case <-notify:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (g *liveLeaseGate) releaseRead() {
	g.mu.Lock()
	if g.readers > 0 {
		g.readers--
	}
	if g.readers == 0 {
		g.signalLocked()
	}
	g.mu.Unlock()
}

func (g *liveLeaseGate) acquireWrite(ctx context.Context) error {
	if ctx == nil {
		return errors.New("storage view write lease context is required")
	}
	g.mu.Lock()
	g.waitingWriters++
	g.mu.Unlock()
	for {
		g.mu.Lock()
		if !g.writer && g.readers == 0 {
			g.writer = true
			g.waitingWriters--
			g.mu.Unlock()
			return nil
		}
		notify := g.notify
		g.mu.Unlock()
		select {
		case <-notify:
		case <-ctx.Done():
			g.mu.Lock()
			g.waitingWriters--
			g.signalLocked()
			g.mu.Unlock()
			return ctx.Err()
		}
	}
}

func (g *liveLeaseGate) releaseWrite() {
	g.mu.Lock()
	g.writer = false
	g.signalLocked()
	g.mu.Unlock()
}

func (g *liveLeaseGate) signalLocked() {
	close(g.notify)
	g.notify = make(chan struct{})
}
