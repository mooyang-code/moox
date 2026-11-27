package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/rs/xid"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	maxTaskResultInventoryEntries          = 1000
	maxTaskResultInventoryPage             = 100
	taskResultInventoryMetadataConcurrency = 16
	taskResultInventorySnapshotTTL         = 2 * time.Minute
)

type taskResultInventoryToken struct {
	observedMs int64
	pageSize   int
	nonce      string
	digest     string
}

// GetTaskResultInventory 无状态返回当前空间的任务结果清单。
// 首页请求生成自描述的 snapshot_id（含观察时刻、页大小与清单内容指纹）；后续翻页带回该 snapshot_id，
// 服务端无状态重算内容指纹并校验一致，既不占用进程内快照缓存，又能防止翻页期间静默混拼两代清单。
func (s *Service) GetTaskResultInventory(ctx context.Context, req *pb.GetTaskResultInventoryReq) (*pb.GetTaskResultInventoryRsp, error) {
	if req == nil {
		return taskResultInventoryError(pb.ErrorCode_INVALID_PARAM, "request is required"), nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if spaceID == "" {
		return taskResultInventoryError(pb.ErrorCode_INVALID_PARAM, "space_id is required"), nil
	}
	page, size := 1, maxTaskResultInventoryPage
	if requested := req.GetPage(); requested != nil {
		if requested.GetPage() > 0 {
			page = int(requested.GetPage())
		}
		if requested.GetSize() > 0 {
			size = int(requested.GetSize())
		}
	}
	if page < 1 || size < 1 || size > maxTaskResultInventoryPage {
		return taskResultInventoryError(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("page size must be between 1 and %d", maxTaskResultInventoryPage)), nil
	}

	now := time.Now().UTC()
	observed := now
	snapshotID := strings.TrimSpace(req.GetSnapshotId())
	var token taskResultInventoryToken
	if snapshotID != "" {
		parsed, ok := parseTaskResultInventoryToken(snapshotID)
		if !ok {
			return taskResultInventoryError(pb.ErrorCode_CONFLICT, "task-result inventory snapshot expired or does not match"), nil
		}
		if parsed.pageSize != size {
			return taskResultInventoryError(pb.ErrorCode_INVALID_PARAM, "page size must match the inventory snapshot"), nil
		}
		tokenObserved := time.UnixMilli(parsed.observedMs).UTC()
		if !now.Before(tokenObserved.Add(taskResultInventorySnapshotTTL)) || tokenObserved.After(now.Add(time.Minute)) {
			return taskResultInventoryError(pb.ErrorCode_CONFLICT, "task-result inventory snapshot expired or does not match"), nil
		}
		token = parsed
		observed = tokenObserved
	}

	allEntries, err := s.collectTaskResultInventoryEntries(ctx, spaceID, observed)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] build task-result inventory failed: %v", err)
		return taskResultInventoryError(pb.ErrorCode_INNER_ERR, err.Error()), nil
	}
	observedMs := unixMillis(observed)
	digest := taskResultInventoryDigest(spaceID, size, observedMs, allEntries)
	if snapshotID != "" {
		if token.digest != digest {
			return taskResultInventoryError(pb.ErrorCode_CONFLICT, "task-result inventory snapshot expired or does not match"), nil
		}
	} else {
		snapshotID = formatTaskResultInventoryToken(observedMs, size, xid.New().String(), digest)
	}

	start := (page - 1) * size
	if start > len(allEntries) {
		start = len(allEntries)
	}
	end := start + size
	if end > len(allEntries) {
		end = len(allEntries)
	}
	entries := append([]*pb.TaskResultInventoryEntry(nil), allEntries[start:end]...)
	return &pb.GetTaskResultInventoryRsp{
		RetInfo: retOK(), Entries: entries, SnapshotId: snapshotID,
		ObservedAt: observedMs,
		Page:       pageResult(page, size, int64(len(allEntries))),
	}, nil
}

func formatTaskResultInventoryToken(observedMs int64, pageSize int, nonce, digest string) string {
	return fmt.Sprintf("%d.%d.%s.%s", observedMs, pageSize, nonce, digest)
}

func parseTaskResultInventoryToken(raw string) (taskResultInventoryToken, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 4 {
		return taskResultInventoryToken{}, false
	}
	observedMs, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || observedMs <= 0 {
		return taskResultInventoryToken{}, false
	}
	pageSize, err := strconv.Atoi(parts[1])
	if err != nil || pageSize < 1 || pageSize > maxTaskResultInventoryPage {
		return taskResultInventoryToken{}, false
	}
	if parts[2] == "" || len(parts[3]) != 32 {
		return taskResultInventoryToken{}, false
	}
	if _, err := hex.DecodeString(parts[3]); err != nil {
		return taskResultInventoryToken{}, false
	}
	return taskResultInventoryToken{
		observedMs: observedMs,
		pageSize:   pageSize,
		nonce:      parts[2],
		digest:     parts[3],
	}, true
}

// taskResultInventoryDigest 计算空间清单的分页拓扑与稳定配置摘要（纳入 observedMs、剔除高频跳变的 ViewLastDataTime），
// 既避免翻页期间正常数据写入误触发 CONFLICT，又防止篡改 observedMs 或混拼增删后的两代清单。
func taskResultInventoryDigest(spaceID string, pageSize int, observedMs int64, entries []*pb.TaskResultInventoryEntry) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00%d\n", spaceID, pageSize, observedMs, len(entries))
	for _, entry := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%t\x00%t\x00%s\x00%d\n",
			entry.GetTaskId(), entry.GetDatasetId(), entry.GetFrequency(),
			entry.GetEnabled(), entry.GetOwnershipVerified(), entry.GetResultStatus(),
			entry.GetAllowedLatenessSeconds(),
		)
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func (s *Service) collectTaskResultInventoryEntries(ctx context.Context, spaceID string, observed time.Time) ([]*pb.TaskResultInventoryEntry, error) {
	if s == nil || s.taskRepo == nil || s.persistence == nil {
		return nil, fmt.Errorf("task-result inventory dependencies are not initialized")
	}
	tasks, err := s.taskRepo.ListKlineResultTasks(ctx, spaceID, maxTaskResultInventoryEntries)
	if err != nil {
		return nil, err
	}
	entries, err := s.buildTaskResultInventoryEntries(ctx, tasks, observed)
	if err != nil {
		return nil, err
	}
	s.applyHistorySeeds(ctx, spaceID, entries)
	return entries, nil
}

// applyHistorySeeds 把结果数据集的历史补齐状态写进清单：补齐按时间升序写历史行，storage-view 按标的记的最新一根
// 只认本进程见过的写入，Monitor 据此在补齐中与刚结束时不按标的报“没有数据、落后”。读不到补齐记录时不写
// （Monitor 照常检查），不让清单因此失败。合成任务的目标数据集没有补齐记录，不标。
func (s *Service) applyHistorySeeds(ctx context.Context, spaceID string, entries []*pb.TaskResultInventoryEntry) {
	if s.klineOps == nil || len(entries) == 0 {
		return
	}
	views, err := s.klineOps.SeedViews(ctx, spaceID, false)
	if err != nil {
		log.WarnContextf(ctx, "[Collector] task-result inventory: list history seeds failed space=%s: %v", spaceID, err)
		return
	}
	seeds := make(map[string]store.HistorySeed, len(views))
	for _, view := range views {
		seeds[view.DatasetID+"|"+view.Frequency] = view.HistorySeed
	}
	for _, entry := range entries {
		seed, ok := seeds[entry.GetDatasetId()+"|"+entry.GetFrequency()]
		switch {
		case !ok:
		case seed.Active():
			entry.HistorySeeding = true
		case seed.FinishedAt != nil:
			entry.HistorySeedFinishedAt = seed.FinishedAt.UnixMilli()
		}
	}
}

func (s *Service) buildTaskResultInventoryEntries(ctx context.Context, tasks []domain.CollectionTask, observed time.Time) ([]*pb.TaskResultInventoryEntry, error) {
	entries := make([]*pb.TaskResultInventoryEntry, len(tasks))
	if len(tasks) == 0 {
		return entries, nil
	}
	workerCount := min(taskResultInventoryMetadataConcurrency, len(tasks))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var workers sync.WaitGroup
	var errorMu sync.Mutex
	var firstErr error
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-workCtx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					if workCtx.Err() != nil {
						return
					}
					entry, err := s.taskResultInventoryEntry(workCtx, tasks[index], observed)
					if err != nil {
						errorMu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("task %s/%s: %w", tasks[index].SpaceID, tasks[index].TaskID, err)
							cancel()
						}
						errorMu.Unlock()
						return
					}
					entries[index] = entry
				}
			}
		}()
	}
dispatch:
	for index := range tasks {
		select {
		case <-workCtx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	workers.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Service) taskResultInventoryEntry(ctx context.Context, task domain.CollectionTask, observed time.Time) (*pb.TaskResultInventoryEntry, error) {
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	if err != nil {
		return nil, fmt.Errorf("parse collect params: %w", err)
	}
	frequency := strings.TrimSpace(params.Frequency)
	if strings.EqualFold(task.DataType, "kline_resample") {
		frequency = strings.TrimSpace(params.TargetFrequency)
	}
	if frequency == "" {
		return nil, fmt.Errorf("task result frequency is required")
	}
	marketID := firstTaskResultInventoryValue(params.MarketID, task.SpaceID)
	calendarID, timezone, sessions, err := s.taskResultCalendar(ctx, task, params, marketID)
	if err != nil {
		return nil, err
	}
	latenessSeconds, err := allowedLatenessSeconds(task.DataType, params, task.SpaceID)
	if err != nil {
		return nil, err
	}
	ids := collectionTaskResultIDs(task)
	entry := &pb.TaskResultInventoryEntry{
		SpaceId: task.SpaceID, TaskId: task.TaskID, DatasetId: ids.DatasetID,
		Frequency: frequency, MarketId: marketID, CalendarId: calendarID, Timezone: timezone,
		Sessions: sessions, ObservedAt: unixMillis(observed), Enabled: task.Enabled,
		ResultStatus: "pending", AllowedLatenessSeconds: int32(latenessSeconds),
	}
	if !task.Enabled {
		return entry, nil
	}
	if task.PrepareState == domain.PrepareStateError {
		entry.ResultStatus = "error"
		return entry, nil
	}
	if task.PrepareState != domain.PrepareStateReady {
		return entry, nil
	}
	if s.resultManager == nil {
		return nil, fmt.Errorf("task result metadata manager is not configured")
	}
	inspection, err := s.resultManager.InspectIDs(ctx, task.SpaceID, task.TaskID, ids)
	if err != nil {
		return nil, err
	}
	entry.ResultStatus = inspection.Status
	entry.OwnershipVerified = inspection.Dataset != nil
	entry.ViewLastDataTime = strings.TrimSpace(inspection.LastDataTime)
	if strings.EqualFold(task.DataType, "kline_resample") && inspection.Dataset != nil {
		if dsMarket, dsErr := barclock.MarketForDataset(inspection.Dataset.GetAlignment(), inspection.Dataset.GetAttributes()["calendar"]); dsErr == nil {
			entry.CalendarId, entry.Timezone, entry.Sessions = marketCalendarTuple(dsMarket)
		}
	}
	// 优先采用 Storage 数据集上真实生效的 allowed_lateness_seconds（如重采样目标的 120s 或同步生效的自定义迟到时间）。
	if effective := inspection.Dataset.GetAllowedLatenessSeconds(); effective > 0 {
		entry.AllowedLatenessSeconds = int32(effective)
	}
	return entry, nil
}

func (s *Service) taskResultCalendar(ctx context.Context, task domain.CollectionTask, params *domain.CollectParams, marketID string) (calendarID, timezone string, sessions []string, err error) {
	if strings.EqualFold(task.DataType, "kline_resample") && s != nil && s.datasetSrc != nil && strings.TrimSpace(params.SourceDatasetID) != "" {
		if sourceInfo, sourceErr := s.datasetSrc.GetDataset(ctx, task.SpaceID, params.SourceDatasetID); sourceErr == nil {
			if market, marketErr := barclock.MarketForDataset(sourceInfo.Alignment, sourceInfo.Attributes["calendar"]); marketErr == nil {
				c, tz, sess := marketCalendarTuple(market)
				return c, tz, sess, nil
			}
		}
	}
	return taskResultMarketCalendar(marketID)
}

func marketCalendarTuple(market barclock.Market) (calendarID, timezone string, sessions []string) {
	if market.Alignment == barclock.AlignmentEpochUTC {
		return "", "UTC", nil
	}
	return market.CalendarID, market.Timezone, append([]string(nil), market.Sessions...)
}

// allowedLatenessSeconds 返回任务结果数据集的兜底允许迟到时间（在 Storage 数据集尚未创建或未设置时使用）：
// 任务参数优先；重采样任务默认取 TargetLatenessSeconds（与建表写入一致）；其余任务取市场 market.yaml 默认值；市场未登记时报错。
func allowedLatenessSeconds(dataType string, params *domain.CollectParams, spaceID string) (int, error) {
	if params != nil && params.AllowedLatenessSeconds > 0 {
		return params.AllowedLatenessSeconds, nil
	}
	if strings.EqualFold(strings.TrimSpace(dataType), "kline_resample") {
		return collectorresample.TargetLatenessSeconds, nil
	}
	market, err := markets.ConfigFor(spaceID)
	if err != nil {
		return 0, err
	}
	return market.AllowedLatenessSeconds, nil
}

// taskResultMarketCalendar 返回市场的交易日历、时区与交易时段（取自 barclock 的市场登记），Monitor 据此判断应收的 K 线；
// 市场未登记时报错，不当成连续交易。
func taskResultMarketCalendar(marketID string) (calendarID, timezone string, sessions []string, err error) {
	market, err := barclock.MarketFor(marketID)
	if err != nil {
		return "", "", nil, err
	}
	if market.Alignment == barclock.AlignmentEpochUTC {
		return "", "UTC", nil, nil
	}
	return market.CalendarID, market.Timezone, append([]string(nil), market.Sessions...), nil
}

func firstTaskResultInventoryValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func taskResultInventoryError(code pb.ErrorCode, message string) *pb.GetTaskResultInventoryRsp {
	return &pb.GetTaskResultInventoryRsp{RetInfo: retErr(code, message), Entries: []*pb.TaskResultInventoryEntry{}}
}
