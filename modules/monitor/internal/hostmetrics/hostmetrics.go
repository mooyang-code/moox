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
	"google.golang.org/protobuf/proto"
)

const SpaceID = "moox_system"

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
	writer   SnapshotWriter
	reader   HistoryReader
	alert    *AlertEvaluator
	registry *Registry
	presence PresenceTransitionSink
	ready    func() bool
	mu       sync.RWMutex
	latest   map[string]AgentView
}

func NewStore(writer SnapshotWriter, reader HistoryReader) *Store {
	return &Store{writer: writer, reader: reader, latest: make(map[string]AgentView)}
}

func (s *Store) SetAlertEvaluator(evaluator *AlertEvaluator) {
	if s != nil {
		s.alert = evaluator
	}
}

func (s *Store) SetRegistry(registry *Registry) {
	if s != nil {
		s.registry = registry
	}
}

func (s *Store) SetPresenceTransitionSink(sink PresenceTransitionSink) {
	if s != nil {
		s.presence = sink
	}
}

func (s *Store) SetStorageReady(ready func() bool) {
	if s != nil {
		s.ready = ready
	}
}

func (s *Store) StorageReady() bool { return s != nil && (s.ready == nil || s.ready()) }

func ValidateMessage(msg *eventpb.EventMessage) (*hostmetricpb.HostMetric, error) {
	if msg == nil {
		return nil, errors.New("message is nil")
	}
	if msg.GetEventName() != events.ObservabilityHostSnapshotReported.Name() || msg.GetEventVersion() != events.ObservabilityHostSnapshotReported.Version() {
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

func (s *Store) Persist(ctx context.Context, msg *eventpb.EventMessage, metric *hostmetricpb.HostMetric) error {
	if s == nil {
		return errors.New("host metric store is nil")
	}
	if msg == nil || metric == nil {
		return errors.New("host metric event is incomplete")
	}
	occurredAt := msg.GetOccurredAt().AsTime().UTC()
	current := true
	if s.registry != nil {
		result, err := s.registry.Observe(ctx, HostObservation{
			AgentID: metric.GetAgentId(), Hostname: metric.GetHostname(), BootID: metric.GetBootId(),
			OccurredAt: occurredAt, EventID: msg.GetEventId(),
		})
		if err != nil {
			return fmt.Errorf("update host agent registry: %w", err)
		}
		current = result.Current
		if result.Transition != nil && s.presence != nil {
			s.presence.HandlePresenceTransition(ctx, *result.Transition)
		}
	}
	if !current {
		return nil
	}
	if !s.StorageReady() {
		return errors.New("host storage schema is not ready")
	}
	if s.writer == nil {
		return errors.New("host metric storage writer is not configured")
	}
	if err := s.writer.WriteSnapshot(ctx, metric.GetSnapshot(), metric.GetAgentId(), occurredAt, msg.GetEventId()); err != nil {
		return fmt.Errorf("write host metric snapshot: %w", err)
	}
	view := AgentView{
		AgentID: metric.GetAgentId(), Hostname: metric.GetHostname(), BootID: metric.GetBootId(),
		LastSeenAt: occurredAt.Format(time.RFC3339Nano), Reachable: true,
		Snapshot: cloneSnapshot(metric.GetSnapshot()),
	}
	s.mu.Lock()
	if s.latest == nil {
		s.latest = make(map[string]AgentView)
	}
	previous, exists := s.latest[view.AgentID]
	previousSeenAt, parseErr := time.Parse(time.RFC3339Nano, previous.LastSeenAt)
	if !exists || parseErr != nil || occurredAt.After(previousSeenAt) {
		s.latest[view.AgentID] = view
	}
	s.mu.Unlock()
	if s.alert != nil {
		_ = s.alert.Evaluate(ctx, metric.GetAgentId(), msg.GetEventId(), metric.GetSnapshot(), occurredAt)
	}
	return nil
}

type AgentView struct {
	AgentID, Hostname, BootID, LastSeenAt string
	Archived                              bool
	Reachable                             bool
	StaleSeconds                          int64
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
	if s.registry != nil {
		presence, err := s.registry.List(ctx, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		s.mu.RLock()
		out := make([]AgentView, 0, len(presence))
		for _, agent := range presence {
			view := AgentView{
				AgentID: agent.AgentID, Hostname: agent.Hostname, BootID: agent.BootID,
				LastSeenAt: agent.LastSeenAt.Format(time.RFC3339Nano),
				Reachable:  agent.Reachable, StaleSeconds: agent.StaleSeconds,
			}
			if latest, ok := s.latest[agent.AgentID]; ok {
				view.Snapshot = cloneSnapshot(latest.Snapshot)
			}
			out = append(out, view)
		}
		s.mu.RUnlock()
		return out, nil
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
