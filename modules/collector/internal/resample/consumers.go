package resample

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	nats "github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskIndex 按（空间、源数据集、源频率）找出就绪的合成任务；任务清单按 TTL 缓存，任务变化最多延迟一个 TTL 生效。
type TaskIndex struct {
	Tasks *store.TaskRepository
	TTL   time.Duration

	mu      sync.Mutex
	cached  map[string]cachedTasks
	nowFunc func() time.Time
}

type cachedTasks struct {
	at    time.Time
	tasks []*Task
}

func (i *TaskIndex) now() time.Time {
	if i.nowFunc != nil {
		return i.nowFunc()
	}
	return time.Now()
}

// Match 返回源数据集与源频率匹配的任务。
func (i *TaskIndex) Match(ctx context.Context, spaceID, datasetID, frequency string) ([]*Task, error) {
	tasks, err := i.space(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	var out []*Task
	for _, task := range tasks {
		if task.Spec.SourceDatasetID == datasetID && task.Source.Storage == frequency {
			out = append(out, task)
		}
	}
	return out, nil
}

func (i *TaskIndex) space(ctx context.Context, spaceID string) ([]*Task, error) {
	ttl := i.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if entry, ok := i.cached[spaceID]; ok && i.now().Sub(entry.at) < ttl {
		return entry.tasks, nil
	}
	collections, err := i.Tasks.ListEnabled(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	var tasks []*Task
	for _, collection := range collections {
		if !strings.EqualFold(collection.DataType, "kline_resample") || collection.PrepareState != domain.PrepareStateReady {
			continue
		}
		task, err := NewTask(collection)
		if err != nil {
			log.WarnContextf(ctx, "[Collector] resample task %s is invalid, skipped: %v", collection.TaskID, err)
			continue
		}
		tasks = append(tasks, task)
	}
	if i.cached == nil {
		i.cached = map[string]cachedTasks{}
	}
	i.cached[spaceID] = cachedTasks{at: i.now(), tasks: tasks}
	return tasks, nil
}

// PeriodHandler 处理源数据集的 CollectorPeriodCompleted：收到桶内最后一个源周期的完成事件，就对该桶按数据集批量合成。
type PeriodHandler struct {
	Registry *events.Registry
	Index    *TaskIndex
	Engine   *Engine
	NakDelay time.Duration
	Metrics  *Metrics
}

// Handle 实现 jetstream.DeliveryHandler。
func (h *PeriodHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil || delivery.DecodeError != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("resample period event is undecodable")}
	}
	message, payload, err := events.DecodeCollectorPeriodCompletedWithContentType(h.Registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil || message == nil || payload == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("reject collector period event: %v", err)}
	}
	if h.Metrics != nil {
		h.Metrics.Events.Inc()
	}
	tasks, err := h.Index.Match(ctx, message.GetSpaceId(), payload.GetDatasetId(), payload.GetFrequency())
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: h.nakDelay(), Err: err}
	}
	period := time.Unix(payload.GetPeriodTime(), 0).UTC()
	failed := make(map[string]struct{}, len(payload.GetFailedSubjects()))
	for _, subject := range payload.GetFailedSubjects() {
		failed[subject] = struct{}{}
	}
	subjects := make([]string, 0, len(payload.GetUniverseSubjectIds()))
	for _, subject := range payload.GetUniverseSubjectIds() {
		if _, bad := failed[subject]; !bad {
			subjects = append(subjects, subject)
		}
	}
	var errs []error
	for _, task := range tasks {
		bucket, ok, err := task.Clock.BucketAt(period)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// 只有桶内最后一个源周期收盘时，桶才收盘；其余源周期的事件不触发。
		if !ok || !bucket.End.Equal(period.Add(task.Source.Duration)) {
			continue
		}
		if _, err := h.Engine.RunBucket(ctx, task, bucket, subjects, ""); err != nil {
			errs = append(errs, fmt.Errorf("task %s: %w", task.Spec.TaskID, err))
		}
	}
	if len(errs) > 0 {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: h.nakDelay(), Err: errors.Join(errs...)}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func (h *PeriodHandler) nakDelay() time.Duration {
	if h.NakDelay > 0 {
		return h.NakDelay
	}
	return 10 * time.Second
}

// BackfilledHandler 处理 event.collector.kline.backfilled：源数据被补写后，对依赖它的合成任务提交历史合成作业。
type BackfilledHandler struct {
	Registry *events.Registry
	Index    *TaskIndex
	State    *store.ResampleStateRepository
	// SettleAfter 是作业最早开始时间相对事件确认时刻的推迟量（2×单次执行硬超时），
	// 保证补采写入之前读过旧源数据的执行都已写完或放弃，不会用旧结果盖掉新结果。
	SettleAfter time.Duration
	NewID       func() string
	Now         func() time.Time
	NakDelay    time.Duration
}

// Handle 实现 jetstream.DeliveryHandler。
func (h *BackfilledHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil || delivery.DecodeError != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("backfilled event is undecodable")}
	}
	message, value, err := events.DecodeRaw(h.Registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil || message == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("reject backfilled event: %v", err)}
	}
	payload, ok := value.(*storagepb.CollectorKlineBackfilled)
	if !ok {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("unexpected backfilled event payload %T", value)}
	}
	tasks, err := h.Index.Match(ctx, message.GetSpaceId(), payload.GetDatasetId(), payload.GetFrequency())
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: h.nakDelay(), Err: err}
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	newID := h.NewID
	if newID == nil {
		newID = func() string { return fmt.Sprintf("rsj-%d", time.Now().UnixNano()) }
	}
	var errs []error
	for _, task := range tasks {
		_, _, err := SubmitJob(ctx, h.State, task, SubmitJobRequest{
			SpaceID: task.Spec.SpaceID, TaskID: task.Spec.TaskID, RequestID: message.GetEventId(), Origin: store.ResampleOriginSourceBackfill,
			Subjects: payload.GetSubjectIds(), Start: payload.GetStart().AsTime(), End: payload.GetEnd().AsTime(), NotBefore: payload.GetConfirmedAt().AsTime().Add(h.SettleAfter),
		}, newID, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("task %s: %w", task.Spec.TaskID, err))
		}
	}
	if len(errs) > 0 {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: h.nakDelay(), Err: errors.Join(errs...)}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func (h *BackfilledHandler) nakDelay() time.Duration {
	if h.NakDelay > 0 {
		return h.NakDelay
	}
	return 10 * time.Second
}

// ConsumersConfig 是事件消费者的连接参数。
type ConsumersConfig struct {
	URLs           []string
	CredentialFile string
	SpaceIDs       []string
	FetchMaxWait   time.Duration
	AckWait        time.Duration
	MaxAckPending  int
}

// Consumers 持有 JetStream 连接与每个空间的两个持久消费者。
type Consumers struct {
	client    *jetstream.Client
	consumers []*events.Consumer
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// StartConsumers 连接事件总线并启动源周期完成与补采完成两个消费者（每个空间各一个持久消费者）。
func StartConsumers(ctx context.Context, cfg ConsumersConfig, registry *events.Registry, period *PeriodHandler, backfilled *BackfilledHandler) (*Consumers, error) {
	if len(cfg.SpaceIDs) == 0 {
		return nil, errors.New("resample consumers need at least one space")
	}
	clientCfg := jetstream.ConfigFromEnv(append([]string(nil), cfg.URLs...), "moox-collector")
	if cfg.CredentialFile != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
			return nil, err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	c := &Consumers{client: client, cancel: cancel}
	ackWait, fetchWait, pending := cfg.AckWait, cfg.FetchMaxWait, cfg.MaxAckPending
	if ackWait <= 0 {
		ackWait = 2 * time.Minute
	}
	if fetchWait <= 0 {
		fetchWait = time.Second
	}
	if pending <= 0 {
		pending = 16
	}
	for _, spaceID := range cfg.SpaceIDs {
		for _, spec := range []struct {
			name    string
			event   events.Event
			handler jetstream.DeliveryHandler
		}{
			{"collector_resample_period_" + spaceID + "_v1", events.CollectorPeriodCompleted, period},
			{"collector_resample_backfilled_" + spaceID + "_v1", events.CollectorKlineBackfilled, backfilled},
		} {
			consumer, err := events.NewSpaceConsumer(ctx, client, registry, events.SpaceConsumerConfig{
				ConsumerConfig: events.ConsumerConfig{
					Name: spec.name, Event: spec.event, AckWait: ackWait, MaxDeliver: 10, MaxAckPending: pending,
					FetchMaxWait: fetchWait, DeliverPolicy: nats.DeliverNewPolicy, DeliverDecodeErrors: true,
				},
				SpaceID: spaceID,
			})
			if err != nil {
				_ = c.Close()
				return nil, fmt.Errorf("start consumer %s: %w", spec.name, err)
			}
			c.consumers = append(c.consumers, consumer)
			runner := jetstream.NewRunner(consumer, spec.handler, jetstream.RunnerConfig{
				BatchSize: pending, InProgressInterval: ackWait / 4,
				ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
					log.ErrorContextf(runCtx, "[Collector] resample consumer %s error: %v", spec.name, err)
				}),
			})
			c.wg.Add(1)
			go func(name string) {
				defer c.wg.Done()
				for runCtx.Err() == nil {
					if err := runner.Run(runCtx); err != nil && runCtx.Err() == nil {
						log.ErrorContextf(runCtx, "[Collector] resample consumer %s stopped: %v", name, err)
					}
					select {
					case <-runCtx.Done():
					case <-time.After(time.Second):
					}
				}
			}(spec.name)
		}
	}
	return c, nil
}

// Publisher 返回共用这条连接的事件发布者。
func (c *Consumers) Publisher(registry *events.Registry) (*events.Publisher, error) {
	return events.NewPublisher(c.client, registry)
}

// Close 停止消费并关闭连接。
func (c *Consumers) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		c.cancel()
		for _, consumer := range c.consumers {
			err = errors.Join(err, consumer.Close())
		}
		c.wg.Wait()
		err = errors.Join(err, c.client.Close())
	})
	return err
}
