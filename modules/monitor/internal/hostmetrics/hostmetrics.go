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
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"google.golang.org/protobuf/proto"
)

var ErrInvalidHostMetric = errors.New("invalid host metric")

// Store 处理主机快照：登记最近一次上报、写本地快照历史，并在内存里保留每台主机的最新快照。
// 历史只存在 Monitor 自己的库里（t_monitor_host_samples），不依赖 Storage。
// 这里不评估告警：失联与资源阈值由主机类检查按登记时间和最新快照判断，和其他检查走同一条告警路径。
type Store struct {
	samples  *SampleStore
	registry *Registry
	mu       sync.RWMutex
	latest   map[string]AgentView
}

func NewStore(samples *SampleStore) *Store {
	return &Store{samples: samples, latest: make(map[string]AgentView)}
}

func (s *Store) SetRegistry(registry *Registry) {
	if s != nil {
		s.registry = registry
	}
}

// Report 是一次主机快照上报：HostAgent 每分钟随拉取主机策略那次调用带来。
type Report struct {
	// ID 是 UUIDv7，与采集时间一起用于去重。
	ID          string
	CollectedAt time.Time
	Metric      *hostmetricpb.HostMetric
}

// ValidateReport 校验一次上报：ID、采集时刻（毫秒，允许 15 分钟前到 2 分钟后的时钟偏差）、主机 ID 与快照内容。
func ValidateReport(id string, collectedAtMs int64, metric *hostmetricpb.HostMetric) (Report, error) {
	if parsed, err := uuid.Parse(id); err != nil || parsed.Version() != 7 {
		return Report{}, errors.New("主机快照的上报 ID 必须是 UUIDv7")
	}
	if collectedAtMs <= 0 {
		return Report{}, errors.New("主机快照缺少采集时刻")
	}
	occurred := time.UnixMilli(collectedAtMs).UTC()
	now := time.Now().UTC()
	if occurred.Before(now.Add(-15*time.Minute)) || occurred.After(now.Add(2*time.Minute)) {
		return Report{}, errors.New("主机快照的采集时间超出允许的时钟偏差")
	}
	if metric == nil || metric.GetSnapshot() == nil {
		return Report{}, errors.New("主机快照缺少内容")
	}
	if !servicecatalog.ValidHostID(metric.GetHostId()) || metric.GetHostname() == "" {
		return Report{}, errors.New("主机快照的 host_id 不是合法的主机 ID，或缺少主机名")
	}
	if err := validateSnapshot(metric.GetSnapshot()); err != nil {
		return Report{}, err
	}
	return Report{ID: id, CollectedAt: occurred.UTC(), Metric: metric}, nil
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
		if n.GetErrorRateAvailable() {
			for _, value := range []float64{n.GetReceiveErrorsPerSecond(), n.GetTransmitErrorsPerSecond()} {
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					return errors.New("network error rate is invalid")
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

func (s *Store) Persist(ctx context.Context, report Report) error {
	if s == nil {
		return errors.New("主机快照处理未初始化")
	}
	metric := report.Metric
	if metric == nil {
		return errors.New("主机快照上报不完整")
	}
	occurredAt := report.CollectedAt.UTC()
	current := true
	hostID := metric.GetHostId()
	if s.registry != nil {
		result, err := s.registry.Observe(ctx, HostObservation{
			HostID: hostID, Hostname: metric.GetHostname(), BootID: metric.GetBootId(),
			OccurredAt: occurredAt, EventID: report.ID,
		})
		if err != nil {
			return fmt.Errorf("update host agent registry: %w", err)
		}
		current = result.Current
	}
	if !current {
		return nil
	}
	// 顺序：登记最近一次上报（上面）→ 更新最新快照 → 写本地历史。历史写失败只让这条消息重投，
	// 在线判断与阈值检查用的是登记时间和最新快照，不受影响。
	view := AgentView{
		HostID: hostID, Hostname: metric.GetHostname(), BootID: metric.GetBootId(),
		LastSeenAt: occurredAt, Reachable: true,
		Snapshot: cloneSnapshot(metric.GetSnapshot()),
	}
	s.mu.Lock()
	if s.latest == nil {
		s.latest = make(map[string]AgentView)
	}
	previous, exists := s.latest[view.HostID]
	if !exists || occurredAt.After(previous.LastSeenAt) {
		s.latest[view.HostID] = view
	}
	s.mu.Unlock()
	if err := s.samples.Write(ctx, hostID, occurredAt, metric.GetSnapshot()); err != nil {
		return fmt.Errorf("写入主机快照历史: %w", err)
	}
	return nil
}

type AgentView struct {
	HostID, Hostname, BootID string
	LastSeenAt               time.Time
	Reachable                bool
	StaleSeconds             int64
	Snapshot                 *hostmetricpb.HostSnapshot
}

type HistoryPoint struct {
	HostID     string
	ObservedAt time.Time
	Snapshot   *hostmetricpb.HostSnapshot
}

func (s *Store) ListAgents(ctx context.Context) ([]AgentView, error) {
	return s.ListAgentsAt(ctx, time.Now().UTC())
}

func (s *Store) ListAgentsAt(ctx context.Context, now time.Time) ([]AgentView, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("主机快照处理未初始化")
	}
	if s.registry != nil {
		presence, err := s.registry.List(ctx, now)
		if err != nil {
			return nil, err
		}
		s.mu.RLock()
		out := make([]AgentView, 0, len(presence))
		for _, agent := range presence {
			view := AgentView{
				HostID: agent.HostID, Hostname: agent.Hostname, BootID: agent.BootID,
				LastSeenAt: agent.LastSeenAt,
				Reachable:  agent.Reachable, StaleSeconds: agent.StaleSeconds,
			}
			if latest, ok := s.latest[agent.HostID]; ok {
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
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	return out, nil
}

// History 返回 [start, end] 内的快照，按时间升序，最多 limit 份：超过时在整段范围内等间隔抽取，保留首尾；
// 可查范围由调用方按 SampleRetention 截取。
func (s *Store) History(ctx context.Context, hostID string, start, end time.Time, limit int) ([]HistoryPoint, error) {
	if s == nil {
		return nil, errors.New("主机快照处理未初始化")
	}
	return s.samples.History(ctx, hostID, start, end, limit)
}

// DailyLatestHistory 返回 [start, end] 内每个 UTC 自然日最晚的一份快照（按时间升序），供磁盘写满预测使用。
func (s *Store) DailyLatestHistory(ctx context.Context, hostID string, start, end time.Time) ([]HistoryPoint, error) {
	if s == nil {
		return nil, errors.New("主机快照处理未初始化")
	}
	return s.samples.DailyLatestHistory(ctx, hostID, start, end)
}

func cloneSnapshot(snapshot *hostmetricpb.HostSnapshot) *hostmetricpb.HostSnapshot {
	if snapshot == nil {
		return nil
	}
	return proto.Clone(snapshot).(*hostmetricpb.HostSnapshot)
}
