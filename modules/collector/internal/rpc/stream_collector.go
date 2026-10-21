package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// DefaultStreamHeartbeatTimeout 是 collector-stream 心跳的超时：超过视为离线（只用于运行状态、告警与切换前检查）。
	DefaultStreamHeartbeatTimeout = 180 * time.Second
	// DefaultStreamCommitReserveMS 是下发给周期闸门的提交预留。
	DefaultStreamCommitReserveMS = 1000
	recentStreamPeriods          = 60
)

// StreamSettings 是推送采集的 Collector 侧配置（app.yaml 的 stream 段）。
type StreamSettings struct {
	HeartbeatTimeout time.Duration
	CommitReserveMS  int
}

// StreamService 实现 StreamCollector：collector-stream 的心跳、拉计划与周期统计上报。不设租约与写授权：
// 提交是幂等的，计划里的份就是唯一的“授权”。
type StreamService struct {
	pb.UnimplementedStreamCollector
	persistence *store.Store
	settings    StreamSettings
	metrics     *marketfetch.StreamMetrics
	syncer      SubjectSyncer
	now         func() time.Time

	mu      sync.Mutex
	recent  map[string][]*pb.StreamPeriodStat
	streaks map[string]int
	syncing map[string]bool
}

// NewStreamService 构造服务；syncer 用于疑似下架时立即同步标签（可为空）。
func NewStreamService(persistence *store.Store, settings StreamSettings, metrics *marketfetch.StreamMetrics, syncer SubjectSyncer) *StreamService {
	if settings.HeartbeatTimeout <= 0 {
		settings.HeartbeatTimeout = DefaultStreamHeartbeatTimeout
	}
	if settings.CommitReserveMS <= 0 {
		settings.CommitReserveMS = DefaultStreamCommitReserveMS
	}
	return &StreamService{persistence: persistence, settings: settings, metrics: metrics, syncer: syncer, now: time.Now,
		recent: map[string][]*pb.StreamPeriodStat{}, streaks: map[string]int{}, syncing: map[string]bool{}}
}

// streamWorkerStatus 是 t_collector_stream_workers.c_status 的内容。
type streamWorkerStatus struct {
	Version       string                 `json:"version,omitempty"`
	ClockOffsetMS int64                  `json:"clock_offset_ms"`
	Conns         []*pb.StreamConnStatus `json:"conns,omitempty"`
}

func normalizeSpace(spaceID string) string { return strings.ToLower(strings.TrimSpace(spaceID)) }

// planState 返回该空间此刻要求 collector-stream 应用的计划版本与各项。
func (s *StreamService) planState(ctx context.Context, spaceID string) (int64, string, []marketfetch.StreamPlanItem, error) {
	plans := s.persistence.FetchPlans()
	open, err := plans.Open(ctx, spaceID)
	if err != nil {
		return 0, "", nil, err
	}
	applied, err := plans.LatestApplied(ctx, spaceID)
	if err != nil {
		return 0, "", nil, err
	}
	paused, err := plans.PausedSpaces(ctx)
	if err != nil {
		return 0, "", nil, err
	}
	return marketfetch.StreamPlanState(open, applied, paused[spaceID])
}

// StreamHeartbeat 记录心跳与已应用的计划版本，返回最新版本。
func (s *StreamService) StreamHeartbeat(ctx context.Context, req *pb.StreamHeartbeatReq) (*pb.StreamHeartbeatRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	if spaceID == "" || strings.TrimSpace(req.GetInstanceId()) == "" {
		return &pb.StreamHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 与 instance_id 必填")}, nil
	}
	now := s.now().UTC()
	offset := time.Duration(0)
	if req.GetSentAtMs() > 0 {
		offset = time.UnixMilli(req.GetSentAtMs()).Sub(now)
	}
	status := streamWorkerStatus{Version: req.GetVersion(), ClockOffsetMS: offset.Milliseconds(), Conns: req.GetConns()}
	applied := marketfetch.StreamApplied{ID: req.GetAppliedPlanId(), Stage: req.GetAppliedStage()}
	if err := s.persistence.StreamWorkers().Heartbeat(ctx, store.StreamWorker{SpaceID: spaceID, InstanceID: req.GetInstanceId(), Applied: mustMarshal(applied), Status: mustMarshal(status), HeartbeatAt: now}); err != nil {
		return &pb.StreamHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	reconnects := map[[2]string]int64{}
	for _, conn := range req.GetConns() {
		reconnects[[2]string{conn.GetMarket(), conn.GetFrequency()}] += conn.GetReconnects()
	}
	s.metrics.ObserveHeartbeat(spaceID, now, offset, reconnects)
	id, stage, _, err := s.planState(ctx, spaceID)
	if err != nil {
		return &pb.StreamHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.StreamHeartbeatRsp{RetInfo: retOK(), PlanId: id, Stage: stage, ServerTimeMs: now.UnixMilli()}, nil
}

// PullStreamPlan 返回分给推送与本地执行端的计划项。
func (s *StreamService) PullStreamPlan(ctx context.Context, req *pb.PullStreamPlanReq) (*pb.PullStreamPlanRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.PullStreamPlanRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 必填")}, nil
	}
	id, stage, items, err := s.planState(ctx, spaceID)
	if err != nil {
		return &pb.PullStreamPlanRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.PullStreamPlanRsp{RetInfo: retOK(), PlanId: id, Stage: stage, CommitReserveMs: int32(s.settings.CommitReserveMS)}
	market := strings.ToLower(strings.TrimSpace(req.GetMarket()))
	for _, item := range items {
		if market != "" && !strings.EqualFold(item.Market, market) {
			continue
		}
		raw, err := json.Marshal(item.Plan)
		if err != nil {
			return &pb.PullStreamPlanRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		rsp.Items = append(rsp.Items, &pb.StreamPlanItem{Name: item.Name, Executor: item.Executor, Market: item.Market, PlanJson: string(raw)})
	}
	return rsp, nil
}

// ReportStreamPeriods 把周期统计记进指标与最近记录（不落库）；带疑似下架的标的时立即同步相应市场的自动标签。
func (s *StreamService) ReportStreamPeriods(ctx context.Context, req *pb.ReportStreamPeriodsReq) (*pb.ReportStreamPeriodsRsp, error) {
	spaceID := normalizeSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.ReportStreamPeriodsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 必填")}, nil
	}
	s.mu.Lock()
	for _, period := range req.GetPeriods() {
		key := strings.Join([]string{spaceID, period.GetMarket(), period.GetFrequency(), period.GetDatasetId()}, "|")
		if period.GetMissing()-period.GetSuspected() > 0 || !period.GetCommitted() {
			s.streaks[key]++
		} else {
			s.streaks[key] = 0
		}
		s.metrics.ObservePeriod(spaceID, marketfetch.StreamPeriodObservation{
			Market: period.GetMarket(), DatasetID: period.GetDatasetId(), Frequency: period.GetFrequency(), Expected: int(period.GetExpected()), Arrived: int(period.GetArrived()),
			Missing: int(period.GetMissing()), Suspected: int(period.GetSuspected()), Committed: period.GetCommitted(),
			CommitDone: time.Duration(period.GetCommitDoneMs()) * time.Millisecond, FailedStreak: s.streaks[key],
		})
		recent := append(s.recent[spaceID], period)
		if len(recent) > recentStreamPeriods {
			recent = recent[len(recent)-recentStreamPeriods:]
		}
		s.recent[spaceID] = recent
	}
	markets := map[string][]string{}
	for _, suspect := range req.GetSuspects() {
		markets[suspect.GetMarket()] = append(markets[suspect.GetMarket()], suspect.GetSubjectId()+"（"+suspect.GetReason()+"）")
	}
	s.mu.Unlock()
	for market, subjects := range markets {
		log.WarnContextf(ctx, "collector-stream suspects delisted subjects space=%s market=%s subjects=%s", spaceID, market, strings.Join(subjects, ","))
		s.syncMarket(spaceID, market)
	}
	return &pb.ReportStreamPeriodsRsp{RetInfo: retOK()}, nil
}

// syncMarket 在后台立即同步一个市场的自动标签（等同 SyncSubjects，不等 cron）；同一市场同一时间只跑一次。
func (s *StreamService) syncMarket(spaceID, market string) {
	if s.syncer == nil || strings.TrimSpace(market) == "" {
		return
	}
	key := spaceID + "|" + market
	s.mu.Lock()
	if s.syncing[key] {
		s.mu.Unlock()
		return
	}
	s.syncing[key] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.syncing, key)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), subjectSyncTimeout)
		defer cancel()
		report, err := s.syncer.SyncNow(ctx, subjectsync.Filter{SpaceID: spaceID, MarketType: market}, false)
		if err != nil {
			log.WarnContextf(ctx, "collector-stream suspect triggered subject sync failed space=%s market=%s: %v", spaceID, market, err)
			return
		}
		for _, tag := range report.Tags {
			log.InfoContextf(ctx, "collector-stream suspect triggered subject sync space=%s tag=%s status=%s subjects=%d %s", spaceID, tag.TagID, tag.Status, tag.Subjects, tag.Message)
		}
	}()
}

// WorkerStatus 返回该空间 collector-stream 实例的状态；没有实例时返回 nil。
func (s *StreamService) WorkerStatus(ctx context.Context, spaceID string) (*pb.StreamWorkerStatus, error) {
	spaceID = normalizeSpace(spaceID)
	worker, err := s.persistence.StreamWorkers().Get(ctx, spaceID)
	if err != nil || worker == nil {
		return nil, err
	}
	online := s.now().Sub(worker.HeartbeatAt) <= s.settings.HeartbeatTimeout
	s.metrics.ObserveOnline(spaceID, online)
	var applied marketfetch.StreamApplied
	_ = json.Unmarshal([]byte(worker.Applied), &applied)
	var status streamWorkerStatus
	_ = json.Unmarshal([]byte(worker.Status), &status)
	result := &pb.StreamWorkerStatus{InstanceId: worker.InstanceID, Online: online, HeartbeatAt: worker.HeartbeatAt.UTC().Format(time.RFC3339), AppliedPlanId: applied.ID,
		AppliedStage: applied.Stage, Conns: status.Conns, ClockOffsetMs: status.ClockOffsetMS, Version: status.Version}
	s.mu.Lock()
	recent := append([]*pb.StreamPeriodStat(nil), s.recent[spaceID]...)
	s.mu.Unlock()
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].GetPeriod() > recent[j].GetPeriod() })
	if len(recent) > 20 {
		recent = recent[:20]
	}
	result.Recent = recent
	return result, nil
}

// Online 判断该空间的 collector-stream 心跳在超时之内。
func (s *StreamService) Online(ctx context.Context, spaceID string) (bool, error) {
	status, err := s.WorkerStatus(ctx, spaceID)
	if err != nil {
		return false, err
	}
	return status != nil && status.GetOnline(), nil
}

// RefreshOnline 定时刷新各空间的在线指标（心跳中断时指标也要变）。
func (s *StreamService) RefreshOnline(ctx context.Context, spaces []string) {
	for _, space := range spaces {
		if _, err := s.WorkerStatus(ctx, space); err != nil {
			log.WarnContextf(ctx, "collector-stream status space=%s: %v", space, err)
		}
	}
}

func mustMarshal(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal %T: %v", value, err))
	}
	return string(raw)
}
