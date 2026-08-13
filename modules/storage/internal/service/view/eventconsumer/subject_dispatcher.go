package eventconsumer

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type subjectDeliveryHandler func(context.Context, *jetstream.Delivery, *deliveryHeartbeat) error
type deliveryQueueKey func(*jetstream.Delivery) (string, error)

type subjectDispatcherMetricsHooks struct {
	newHeartbeat func(context.Context, *jetstream.Delivery) *deliveryHeartbeat
	onSubmit     func(*jetstream.Delivery)
	onStart      func(*jetstream.Delivery)
	onFinish     func(*jetstream.Delivery)
}

type subjectQueue struct {
	subject    string
	deliveries []*queuedDelivery
	running    bool
}

type queuedDelivery struct {
	delivery  *jetstream.Delivery
	heartbeat *deliveryHeartbeat
}

// subjectDispatcher is a scheduler rather than a plain worker pool: a
// subject queue is scheduled at most once, so one subject cannot have two
// active handlers.
type subjectDispatcher struct {
	ctx        context.Context
	cancel     context.CancelFunc
	maxWorkers int
	handler    subjectDeliveryHandler
	reporter   jetstream.ErrorReporter
	hooks      subjectDispatcherMetricsHooks
	queueKey   deliveryQueueKey
	ready      chan *subjectQueue
	pending    chan struct{}
	queues     map[string]*subjectQueue
	mu         sync.Mutex
	closed     bool
	wg         sync.WaitGroup
}

func newSubjectDispatcher(parent context.Context, maxWorkers, maxPending int, handler subjectDeliveryHandler, reporter jetstream.ErrorReporter, hooks ...subjectDispatcherMetricsHooks) *subjectDispatcher {
	return newSubjectDispatcherWithKey(parent, maxWorkers, maxPending, handler, reporter, nil, hooks...)
}

func newSubjectDispatcherWithKey(parent context.Context, maxWorkers, maxPending int, handler subjectDeliveryHandler, reporter jetstream.ErrorReporter, queueKey deliveryQueueKey, hooks ...subjectDispatcherMetricsHooks) *subjectDispatcher {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if maxPending < 1 {
		maxPending = maxWorkers
	}
	ctx, cancel := context.WithCancel(parent)
	var metricsHooks subjectDispatcherMetricsHooks
	if len(hooks) > 0 {
		metricsHooks = hooks[0]
	}
	d := &subjectDispatcher{
		ctx: ctx, cancel: cancel, maxWorkers: maxWorkers, handler: handler, reporter: reporter,
		hooks: metricsHooks, queueKey: queueKey,
		ready: make(chan *subjectQueue, maxWorkers), pending: make(chan struct{}, maxPending), queues: make(map[string]*subjectQueue),
	}
	d.wg.Add(maxWorkers)
	for i := 0; i < maxWorkers; i++ {
		go d.worker()
	}
	return d
}

func (d *subjectDispatcher) Dispatch(delivery *jetstream.Delivery) error {
	if d == nil || delivery == nil {
		return errors.New("storage view subject delivery is nil")
	}
	select {
	case d.pending <- struct{}{}:
	case <-d.ctx.Done():
		return d.ctx.Err()
	}
	queueName := delivery.Subject
	if d.queueKey != nil {
		var err error
		queueName, err = d.queueKey(delivery)
		if err != nil {
			<-d.pending
			return fmt.Errorf("resolve delivery queue: %w", err)
		}
		if queueName == "" {
			<-d.pending
			return errors.New("delivery queue key is empty")
		}
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		<-d.pending
		return errors.New("storage view subject dispatcher is closed")
	}
	queue := d.queues[queueName]
	if queue == nil {
		queue = &subjectQueue{subject: queueName}
		d.queues[queueName] = queue
	}
	var heartbeat *deliveryHeartbeat
	if d.hooks.newHeartbeat != nil {
		heartbeat = d.hooks.newHeartbeat(d.ctx, delivery)
	}
	queue.deliveries = append(queue.deliveries, &queuedDelivery{delivery: delivery, heartbeat: heartbeat})
	start := !queue.running
	if start {
		queue.running = true
	}
	d.mu.Unlock()
	if d.hooks.onSubmit != nil {
		d.hooks.onSubmit(delivery)
	}
	if start {
		select {
		case d.ready <- queue:
		case <-d.ctx.Done():
			return d.ctx.Err()
		}
	}
	return nil
}

func datasetQueueKey(registry *events.Registry, delivery *jetstream.Delivery) (string, error) {
	if delivery == nil {
		return "", errors.New("delivery is nil")
	}
	if delivery.DecodeError != nil {
		// Keep malformed deliveries in a deterministic subject queue so the
		// normal delivery policy can heartbeat/retry/TERM them. Dispatch must
		// not drop the pending message before policy classification runs.
		if key, ok := subjectDatasetQueueKey(delivery.Subject); ok {
			return key, nil
		}
		return delivery.Subject, nil
	}
	message, _, err := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		if key, ok := subjectDatasetQueueKey(delivery.Subject); ok {
			return key, nil
		}
		return delivery.Subject, nil
	}
	if message == nil {
		if key, ok := subjectDatasetQueueKey(delivery.Subject); ok {
			return key, nil
		}
		return delivery.Subject, nil
	}
	if message.GetSpaceId() == "" || message.GetSubjectId() == "" {
		return delivery.Subject, nil
	}
	return message.GetSpaceId() + "\x00" + message.GetSubjectId(), nil
}

// subjectDatasetQueueKey is deliberately independent of payload decoding. A
// malformed row still has a governed event subject, and must share the same
// Dataset queue as a valid period/sync marker so a poison message cannot be
// bypassed by the marker lane.
func subjectDatasetQueueKey(subject string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(subject), ".")
	if len(parts) < 5 || parts[0] != "moox" {
		return "", false
	}
	decode := func(token string) (string, bool) {
		if token == "" {
			return "", false
		}
		value, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(token))
		return string(value), err == nil && len(value) > 0
	}
	spaceID, ok := decode(parts[len(parts)-2])
	if !ok {
		return "", false
	}
	datasetID, ok := decode(parts[len(parts)-1])
	if !ok {
		return "", false
	}
	return spaceID + "\x00" + datasetID, true
}

func (d *subjectDispatcher) worker() {
	defer d.wg.Done()
	for {
		select {
		case queue := <-d.ready:
			if queue == nil {
				continue
			}
			item, ok := d.next(queue)
			if !ok {
				continue
			}
			delivery := item.delivery
			if d.hooks.onStart != nil {
				d.hooks.onStart(delivery)
			}
			if d.handler != nil {
				if err := d.handler(d.ctx, delivery, item.heartbeat); err != nil && d.ctx.Err() == nil && d.reporter != nil {
					d.reporter.Report(fmt.Errorf("storage view subject %q delivery failed: %w", queue.subject, err))
				}
			}
			if item.heartbeat != nil {
				item.heartbeat.stop()
			}
			if d.hooks.onFinish != nil {
				d.hooks.onFinish(delivery)
			}
			<-d.pending
			d.finish(queue)
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *subjectDispatcher) next(queue *subjectQueue) (*queuedDelivery, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(queue.deliveries) == 0 {
		queue.running = false
		delete(d.queues, queue.subject)
		return nil, false
	}
	delivery := queue.deliveries[0]
	queue.deliveries = queue.deliveries[1:]
	return delivery, true
}

func (d *subjectDispatcher) finish(queue *subjectQueue) {
	d.mu.Lock()
	if len(queue.deliveries) == 0 {
		queue.running = false
		delete(d.queues, queue.subject)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	select {
	case d.ready <- queue:
	default:
		// Never let all workers block trying to requeue while ready already
		// contains other queues. The helper exits with the dispatcher.
		go d.enqueue(queue)
	}
}

func (d *subjectDispatcher) enqueue(queue *subjectQueue) {
	select {
	case d.ready <- queue:
	case <-d.ctx.Done():
	}
}

func (d *subjectDispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	var queued []*deliveryHeartbeat
	if !d.closed {
		d.closed = true
		d.cancel()
		for _, queue := range d.queues {
			for _, item := range queue.deliveries {
				if item != nil && item.heartbeat != nil {
					queued = append(queued, item.heartbeat)
				}
			}
			queue.deliveries = nil
		}
	}
	d.mu.Unlock()
	for _, heartbeat := range queued {
		heartbeat.stop()
	}
	d.wg.Wait()
}
