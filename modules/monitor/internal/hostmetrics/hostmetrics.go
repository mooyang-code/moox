package hostmetrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	Stream       = "MOOX_METRICS"
	ConsumerName = "monitor_hostmetrics_ingest_v1"
	SpaceID      = "moox_system"
)

var Topic = governedFamily(events.MetricsHostReported)

func governedFamily(event events.Event) string {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return ""
	}
	family, err := registry.FamilyPattern(event)
	if err != nil {
		return ""
	}
	return family
}

var ErrInvalidHostMetric = errors.New("invalid host metric")

// SnapshotWriter 是主机指标写入链路唯一的持久化依赖。Monitor 不在 SQLite
// 保存主机样本；Storage 保存短期历史，本组件仅保留最新的内存视图。
type SnapshotWriter interface {
	WriteSnapshot(context.Context, *hostmetricpb.HostSnapshot, string, time.Time, string) error
}

type HistoryReader interface {
	History(context.Context, string, time.Time, time.Time, int) ([]HistoryPoint, error)
}

type Store struct {
	writer SnapshotWriter
	reader HistoryReader
	alert  *AlertEvaluator
	ready  func() bool
	mu     sync.RWMutex
	latest map[string]AgentView
}

// NewStore accepts an interface value to keep old bootstrap call sites
// compiling while deployments migrate to NewStoreWithWriter. Non-writer
// values (including the former *gorm.DB argument) are deliberately ignored.
func NewStore(writer any) *Store {
	var snapshotWriter SnapshotWriter
	if w, ok := writer.(SnapshotWriter); ok {
		snapshotWriter = w
	}
	return NewStoreWithWriter(snapshotWriter)
}

func NewStoreWithWriter(writer SnapshotWriter) *Store {
	return NewStoreWithWriterReader(writer, nil)
}

func NewStoreWithWriterReader(writer SnapshotWriter, reader HistoryReader) *Store {
	return &Store{writer: writer, reader: reader, latest: make(map[string]AgentView)}
}

func (s *Store) SetAlertEvaluator(evaluator *AlertEvaluator) {
	if s != nil {
		s.alert = evaluator
	}
}

func (s *Store) SetStorageReady(ready func() bool) {
	if s != nil {
		s.ready = ready
	}
}

func (s *Store) StorageReady() bool { return s != nil && (s.ready == nil || s.ready()) }

// EnsureSchema is retained as a no-op during the deployment migration. Host
// sample tables are no longer created or read.
func (s *Store) EnsureSchema() error { return nil }

func ValidateMessage(msg *eventpb.EventMessage) (*hostmetricpb.HostMetric, error) {
	if msg == nil {
		return nil, errors.New("message is nil")
	}
	if msg.GetEventName() != events.MetricsHostReported.Name() || msg.GetEventVersion() != events.MetricsHostReported.Version() {
		return nil, errors.New("host metric envelope contract mismatch")
	}
	if msg.GetSpaceId() != SpaceID || strings.TrimSpace(msg.GetSubjectId()) == "" {
		return nil, errors.New("host metric space or sequence mismatch")
	}
	if parsed, err := uuid.Parse(msg.GetEventId()); err != nil || parsed.Version() != 7 {
		return nil, errors.New("host metric message_id must be UUIDv7")
	}
	if msg.GetOccurredAt() == nil || !msg.GetOccurredAt().IsValid() {
		return nil, errors.New("host metric timestamps are invalid")
	}
	now := time.Now().UTC()
	occurred := msg.GetOccurredAt().AsTime()
	if occurred.Before(now.Add(-15*time.Minute)) || occurred.After(now.Add(2*time.Minute)) {
		return nil, errors.New("host metric occurred_at is outside the accepted clock-skew window")
	}
	if uuid.Validate(msg.GetSubjectId()) != nil {
		return nil, errors.New("host metric producer identity is invalid")
	}
	metric := new(hostmetricpb.HostMetric)
	if err := proto.Unmarshal(msg.GetPayload(), metric); err != nil {
		return nil, fmt.Errorf("decode host metric: %w", err)
	}
	if metric.GetSnapshot() == nil {
		return nil, errors.New("host metric snapshot is missing")
	}
	if metric.GetAgentId() != msg.GetSubjectId() || metric.GetAgentId() == "" || metric.GetHostname() == "" {
		return nil, errors.New("host metric identity is invalid")
	}
	if err := validateSnapshot(metric.GetSnapshot()); err != nil {
		return nil, err
	}
	return metric, nil
}

func validateSnapshot(s *hostmetricpb.HostSnapshot) error {
	if s.GetCpu() == nil || s.GetMemory() == nil {
		return errors.New("host metric cpu/memory are required")
	}
	if s.GetCpu().GetLogicalCores() == 0 {
		return errors.New("logical_cores must be positive")
	}
	if err := percent(s.GetCpu().GetUsagePercent(), s.GetCpu().GetUsageAvailable()); err != nil {
		return err
	}
	if err := percent(s.GetMemory().GetUsagePercent(), true); err != nil {
		return err
	}
	if err := memoryBounds(s.GetMemory()); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, f := range s.GetFilesystems() {
		if strings.TrimSpace(f.GetMountpoint()) == "" {
			return errors.New("filesystem mountpoint is empty")
		}
		if _, ok := seen["fs:"+f.GetDevice()+"\x00"+f.GetMountpoint()]; ok {
			return errors.New("duplicate filesystem key")
		}
		seen["fs:"+f.GetDevice()+"\x00"+f.GetMountpoint()] = struct{}{}
		for _, value := range []uint64{f.GetTotalBytes(), f.GetUsedBytes(), f.GetAvailableBytes()} {
			if value > math.MaxInt64 {
				return errors.New("filesystem counter exceeds int64")
			}
		}
		if err := percent(f.GetUsagePercent(), true); err != nil {
			return err
		}
	}
	seen = map[string]struct{}{}
	for _, d := range s.GetDisks() {
		if d.GetDevice() == "" {
			return errors.New("disk device is empty")
		}
		if _, ok := seen[d.GetDevice()]; ok {
			return errors.New("duplicate disk key")
		}
		seen[d.GetDevice()] = struct{}{}
		for _, value := range []uint64{d.GetReadBytesTotal(), d.GetWriteBytesTotal(), d.GetReadOpsTotal(), d.GetWriteOpsTotal(), d.GetIoTimeMsTotal()} {
			if value > math.MaxInt64 {
				return errors.New("disk counter exceeds int64")
			}
		}
		if d.GetRateAvailable() {
			for _, value := range []float64{d.GetReadBytesPerSecond(), d.GetWriteBytesPerSecond(), d.GetReadIops(), d.GetWriteIops(), d.GetUtilizationPercent()} {
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					return errors.New("disk rate is invalid")
				}
			}
			if d.GetUtilizationPercent() > 100 {
				return errors.New("disk utilization is invalid")
			}
		}
	}
	seen = map[string]struct{}{}
	for _, n := range s.GetNetworks() {
		if n.GetDevice() == "" {
			return errors.New("network device is empty")
		}
		if _, ok := seen[n.GetDevice()]; ok {
			return errors.New("duplicate network key")
		}
		seen[n.GetDevice()] = struct{}{}
		for _, value := range []uint64{n.GetReceiveBytesTotal(), n.GetTransmitBytesTotal(), n.GetReceiveErrorsTotal(), n.GetTransmitErrorsTotal(), n.GetReceiveDroppedTotal(), n.GetTransmitDroppedTotal()} {
			if value > math.MaxInt64 {
				return errors.New("network counter exceeds int64")
			}
		}
		if n.GetRateAvailable() {
			for _, value := range []float64{n.GetReceiveBytesPerSecond(), n.GetTransmitBytesPerSecond()} {
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					return errors.New("network rate is invalid")
				}
			}
		}
	}
	return nil
}

func memoryBounds(m *hostmetricpb.MemoryMetric) error {
	if m.GetTotalBytes() > math.MaxInt64 || m.GetUsedBytes() > math.MaxInt64 || m.GetAvailableBytes() > math.MaxInt64 {
		return errors.New("memory counter exceeds int64")
	}
	if m.GetUsedBytes() > m.GetTotalBytes() || m.GetAvailableBytes() > m.GetTotalBytes() {
		return errors.New("memory counters exceed total")
	}
	return nil
}
func percent(v float64, available bool) error {
	if !available {
		return nil
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return errors.New("metric percentage is invalid")
	}
	return nil
}

func (s *Store) Ingest(ctx context.Context, d *jetstream.Delivery) error {
	if s == nil || d == nil {
		return errors.New("host metric store or delivery is nil")
	}
	decoded := events.DecodeDelivery(mustRegistry(), d)
	if decoded.Err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidHostMetric, decoded.Err)
	}
	metric, err := ValidateMessage(decoded.Message)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidHostMetric, err)
	}
	if err := s.persist(ctx, d, decoded.Message, metric); err != nil {
		return err
	}
	return d.Ack(ctx)
}

func (s *Store) persist(ctx context.Context, d *jetstream.Delivery, msg *eventpb.EventMessage, metric *hostmetricpb.HostMetric) error {
	if s == nil {
		return errors.New("host metric store is nil")
	}
	if !s.StorageReady() {
		return errors.New("host storage schema is not ready")
	}
	if s == nil || s.writer == nil {
		return errors.New("host metric storage writer is not configured")
	}
	if d == nil || msg == nil || metric == nil {
		return errors.New("host metric delivery is incomplete")
	}
	if err := s.writer.WriteSnapshot(ctx, metric.GetSnapshot(), metric.GetAgentId(), msg.GetOccurredAt().AsTime(), msg.GetEventId()); err != nil {
		return fmt.Errorf("write host metric snapshot: %w", err)
	}
	now := time.Now().UTC()
	view := AgentView{AgentID: metric.GetAgentId(), Hostname: metric.GetHostname(), BootID: metric.GetBootId(), LastSeenAt: now.Format(time.RFC3339Nano), Snapshot: cloneSnapshot(metric.GetSnapshot())}
	s.mu.Lock()
	if s.latest == nil {
		s.latest = make(map[string]AgentView)
	}
	s.latest[view.AgentID] = view
	s.mu.Unlock()
	if s.alert != nil {
		_ = s.alert.Evaluate(ctx, metric.GetAgentId(), msg.GetEventId(), metric.GetSnapshot(), msg.GetOccurredAt().AsTime())
	}
	return nil
}

type AgentView struct {
	AgentID, Hostname, BootID, LastSeenAt string
	Archived                              bool
	Snapshot                              *hostmetricpb.HostSnapshot
}

type HistoryPoint struct {
	AgentID, ObservedAt string
	Snapshot            *hostmetricpb.HostSnapshot
}

func (s *Store) ListAgents(ctx context.Context) ([]AgentView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("host metric store is nil")
	}
	s.mu.RLock()
	out := make([]AgentView, 0, len(s.latest))
	for _, view := range s.latest {
		view.Snapshot = cloneSnapshot(view.Snapshot)
		out = append(out, view)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out, nil
}

func (s *Store) History(ctx context.Context, agentID string, start, end time.Time, limit int) ([]HistoryPoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s != nil && s.reader != nil {
		return s.reader.History(ctx, agentID, start, end, limit)
	}
	// History now belongs to Storage. A missing reader is a degraded startup
	// state; return an empty result rather than reading removed SQLite tables.
	return []HistoryPoint{}, nil
}

func (s *Store) HistoryAt(ctx context.Context, agentID string, start, end, now time.Time, limit int) ([]HistoryPoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s != nil && s.reader != nil {
		if reader, ok := s.reader.(interface {
			HistoryAt(context.Context, string, time.Time, time.Time, time.Time, int) ([]HistoryPoint, error)
		}); ok {
			return reader.HistoryAt(ctx, agentID, start, end, now, limit)
		}
		return s.reader.History(ctx, agentID, start, end, limit)
	}
	return []HistoryPoint{}, nil
}

func cloneSnapshot(snapshot *hostmetricpb.HostSnapshot) *hostmetricpb.HostSnapshot {
	if snapshot == nil {
		return nil
	}
	return proto.Clone(snapshot).(*hostmetricpb.HostSnapshot)
}

type Consumer struct {
	pull  *events.Consumer
	store *Store
}

func Bind(ctx context.Context, client *jetstream.Client, store *Store) (*Consumer, error) {
	if client == nil || store == nil {
		return nil, errors.New("host metrics client and store are required")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	pull, err := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
		Name: ConsumerName, Event: events.MetricsHostReported,
		AckWait: time.Minute, MaxDeliver: 3, MaxAckPending: 256,
		FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
		DeliverDecodeErrors: true,
	})
	if err != nil {
		return nil, err
	}
	return &Consumer{pull: pull, store: store}, nil
}
func (c *Consumer) Close() error {
	if c == nil || c.pull == nil {
		return nil
	}
	return c.pull.Close()
}
func (c *Consumer) Run(ctx context.Context) error {
	if c == nil || c.pull == nil || c.store == nil {
		return errors.New("host metrics consumer is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if !c.store.StorageReady() {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}
		runner := jetstream.NewRunner(c.pull, c, jetstream.RunnerConfig{
			BatchSize: 64,
			ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
				if ctx.Err() == nil {
					log.WarnContextf(ctx, "monitor host metrics delivery failed: %v", err)
				}
			}),
		})
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	}
}

func isIdleFetchError(err error) bool {
	return errors.Is(err, nats.ErrTimeout)
}

func (c *Consumer) Handle(ctx context.Context, d *jetstream.Delivery) jetstream.HandlerResult {
	if d == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: errors.New("host metrics delivery is nil")}
	}
	decoded := events.DecodeDelivery(mustRegistry(), d)
	metric, err := ValidateMessage(decoded.Message)
	if decoded.Err != nil {
		err = decoded.Err
	}
	if err != nil {
		log.WarnContextf(ctx, "component=monitor_hostmetrics consumer=%s event_id=%s subject=%s delivery_count=%d decision=term reason=%v",
			ConsumerName, d.RawMessageID, d.Subject, d.DeliveryCount, err)
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if err := c.store.persist(ctx, d, decoded.Message, metric); err != nil {
		// Storage is transient from the consumer's perspective. NAK lets
		// JetStream redeliver up to the durable consumer's MaxDeliver=3.
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: retryDelay(d.DeliveryCount), Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func (c *Consumer) handleDelivery(ctx context.Context, d *jetstream.Delivery) error {
	result := c.Handle(ctx, d)
	return errors.Join(result.Err, jetstream.ApplyHandlerResult(ctx, d, result))
}

func retryDelay(deliveryCount uint64) time.Duration {
	switch {
	case deliveryCount <= 1:
		return time.Second
	case deliveryCount == 2:
		return 5 * time.Second
	default:
		return 15 * time.Second
	}
}

func mustRegistry() *events.Registry {
	registry, _ := events.DefaultRegistry()
	return registry
}
