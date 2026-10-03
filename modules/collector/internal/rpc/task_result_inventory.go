package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/rs/xid"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	maxTaskResultInventoryEntries           = 1000
	maxTaskResultInventoryPage              = 100
	maxTaskResultInventorySnapshotsPerSpace = 8
	maxTaskResultInventorySnapshotsTotal    = 64
	maxTaskResultInventoryBuilds            = 8
	taskResultInventoryMetadataConcurrency  = 16
	taskResultInventorySnapshotTTL          = 2 * time.Minute
)

type taskResultInventorySnapshot struct {
	id        string
	spaceID   string
	pageSize  int
	observed  time.Time
	expiresAt time.Time
	entries   []*pb.TaskResultInventoryEntry
}

// GetTaskResultInventory returns a bounded, ownership-checked task-result
// snapshot. Page one creates the snapshot; later pages must carry its ID, so
// a task deletion or result change cannot silently mix two inventories.
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

	snapshotID := strings.TrimSpace(req.GetSnapshotId())
	var snapshot *taskResultInventorySnapshot
	if snapshotID == "" {
		s.inventorySnapshotMu.Lock()
		s.pruneTaskResultInventorySnapshotsLocked(time.Now().UTC())
		if !s.reserveTaskResultInventoryBuildLocked(spaceID) {
			s.inventorySnapshotMu.Unlock()
			return taskResultInventoryError(pb.ErrorCode_CONFLICT, "task-result inventory snapshot capacity is full for this Space or process"), nil
		}
		s.inventorySnapshotMu.Unlock()

		built, err := s.buildTaskResultInventorySnapshot(ctx, spaceID, size)
		if err != nil {
			s.inventorySnapshotMu.Lock()
			s.releaseTaskResultInventoryBuildLocked(spaceID)
			s.inventorySnapshotMu.Unlock()
			log.ErrorContextf(ctx, "[Collector] build task-result inventory failed: %v", err)
			return taskResultInventoryError(pb.ErrorCode_INNER_ERR, err.Error()), nil
		}
		s.inventorySnapshotMu.Lock()
		s.pruneTaskResultInventorySnapshotsLocked(time.Now().UTC())
		s.releaseTaskResultInventoryBuildLocked(spaceID)
		if s.inventorySnapshots == nil {
			s.inventorySnapshots = make(map[string]*taskResultInventorySnapshot)
		}
		s.inventorySnapshots[built.id] = built
		snapshot = built
		s.inventorySnapshotMu.Unlock()
	} else {
		s.inventorySnapshotMu.Lock()
		s.pruneTaskResultInventorySnapshotsLocked(time.Now().UTC())
		cached := s.inventorySnapshots[snapshotID]
		if cached != nil && cached.id == snapshotID && cached.spaceID == spaceID && time.Now().UTC().Before(cached.expiresAt) {
			snapshot = cached
		}
		s.inventorySnapshotMu.Unlock()
		if snapshot == nil {
			return taskResultInventoryError(pb.ErrorCode_CONFLICT, "task-result inventory snapshot expired or does not match"), nil
		}
		if snapshot.pageSize != size {
			return taskResultInventoryError(pb.ErrorCode_INVALID_PARAM, "page size must match the inventory snapshot"), nil
		}
	}

	start := (page - 1) * size
	if start > len(snapshot.entries) {
		start = len(snapshot.entries)
	}
	end := start + size
	if end > len(snapshot.entries) {
		end = len(snapshot.entries)
	}
	entries := append([]*pb.TaskResultInventoryEntry(nil), snapshot.entries[start:end]...)
	if end == len(snapshot.entries) {
		s.inventorySnapshotMu.Lock()
		if s.inventorySnapshots[snapshot.id] == snapshot {
			delete(s.inventorySnapshots, snapshot.id)
		}
		s.inventorySnapshotMu.Unlock()
	}
	return &pb.GetTaskResultInventoryRsp{
		RetInfo: retOK(), Entries: entries, SnapshotId: snapshot.id,
		ObservedAt: snapshot.observed.Format(time.RFC3339Nano),
		Page:       pageResult(page, size, int64(len(snapshot.entries))),
	}, nil
}

func (s *Service) reserveTaskResultInventoryBuildLocked(spaceID string) bool {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return false
	}
	spaceSnapshots, totalBuilds, spaceBuilds := 0, 0, 0
	for _, snapshot := range s.inventorySnapshots {
		if snapshot == nil {
			continue
		}
		if snapshot.spaceID == spaceID {
			spaceSnapshots++
		}
	}
	for id, builds := range s.inventorySnapshotBuilds {
		totalBuilds += builds
		if id == spaceID {
			spaceBuilds = builds
		}
	}
	if spaceSnapshots+spaceBuilds >= maxTaskResultInventorySnapshotsPerSpace ||
		len(s.inventorySnapshots)+totalBuilds >= maxTaskResultInventorySnapshotsTotal ||
		totalBuilds >= maxTaskResultInventoryBuilds {
		return false
	}
	if s.inventorySnapshotBuilds == nil {
		s.inventorySnapshotBuilds = make(map[string]int)
	}
	s.inventorySnapshotBuilds[spaceID]++
	return true
}

func (s *Service) releaseTaskResultInventoryBuildLocked(spaceID string) {
	if s == nil || s.inventorySnapshotBuilds == nil {
		return
	}
	if s.inventorySnapshotBuilds[spaceID] <= 1 {
		delete(s.inventorySnapshotBuilds, spaceID)
		return
	}
	s.inventorySnapshotBuilds[spaceID]--
}

func (s *Service) pruneTaskResultInventorySnapshotsLocked(now time.Time) {
	for id, snapshot := range s.inventorySnapshots {
		if snapshot == nil || !now.Before(snapshot.expiresAt) {
			delete(s.inventorySnapshots, id)
		}
	}
}

func (s *Service) buildTaskResultInventorySnapshot(ctx context.Context, spaceID string, pageSize int) (*taskResultInventorySnapshot, error) {
	if s == nil || s.taskRepo == nil || s.persistence == nil || s.persistence.PeriodReadiness() == nil {
		return nil, fmt.Errorf("task-result inventory dependencies are not initialized")
	}
	tasks, err := s.taskRepo.ListKlineResultTasks(ctx, spaceID, maxTaskResultInventoryEntries)
	if err != nil {
		return nil, err
	}
	observed := time.Now().UTC()
	entries, err := s.buildTaskResultInventoryEntries(ctx, tasks, observed)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", observed.Format(time.RFC3339Nano), len(entries), xid.New().String())))
	return &taskResultInventorySnapshot{
		id: hex.EncodeToString(checksum[:]), spaceID: spaceID, pageSize: pageSize, observed: observed,
		expiresAt: observed.Add(taskResultInventorySnapshotTTL), entries: entries,
	}, nil
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
	calendarID, timezone, sessions := taskResultMarketCalendar(marketID)
	ids, err := collectionTaskResultIDs(task)
	if err != nil {
		return nil, err
	}
	entry := &pb.TaskResultInventoryEntry{
		SpaceId: task.SpaceID, TaskId: task.TaskID, DatasetId: ids.DatasetID, ViewId: ids.ViewID,
		Frequency: frequency, MarketId: marketID, CalendarId: calendarID, Timezone: timezone,
		Sessions: sessions, ObservedAt: observed.Format(time.RFC3339Nano), Enabled: task.Enabled,
		ResultStatus: "pending",
	}
	if !task.Enabled {
		if err := s.inspectDisabledTaskResult(ctx, entry, task, params, ids); err != nil {
			return nil, err
		}
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
	inspection, err := inspectCollectionTaskResult(ctx, s.resultManager, task, ids)
	if err != nil {
		return nil, err
	}
	entry.ResultStatus = inspection.Status
	entry.OwnershipVerified = inspection.Dataset != nil && inspection.View != nil
	entry.ViewLastDataTime = strings.TrimSpace(inspection.LastDataTime)
	if entry.OwnershipVerified {
		s.setCanaryCandidate(entry, task, params, s.singleTaskCanarySeries(ctx, task))
		if err := s.populateLatestCompletedPeriod(ctx, entry, task, ids.DatasetID, frequency); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

func (s *Service) inspectDisabledTaskResult(ctx context.Context, entry *pb.TaskResultInventoryEntry, task domain.CollectionTask, params *domain.CollectParams, ids taskresult.IDs) error {
	if entry == nil || params == nil || s == nil || s.resultManager == nil {
		return nil
	}
	series := s.singleTaskCanarySeries(ctx, task)
	if series == nil {
		return nil
	}
	inspection, err := inspectCollectionTaskResult(ctx, s.resultManager, task, ids)
	if err != nil {
		entry.ResultStatus = "error"
		return nil
	}
	entry.ResultStatus = inspection.Status
	entry.OwnershipVerified = inspection.Dataset != nil && inspection.View != nil
	entry.ViewLastDataTime = strings.TrimSpace(inspection.LastDataTime)
	if !entry.OwnershipVerified {
		return nil
	}
	s.setCanaryCandidate(entry, task, params, series)
	return s.populateLatestCompletedPeriod(ctx, entry, task, ids.DatasetID, entry.GetFrequency())
}

func (s *Service) singleTaskCanarySeries(ctx context.Context, task domain.CollectionTask) *domain.TaskSeries {
	if s == nil || s.taskRepo == nil {
		return nil
	}
	series, single, err := s.taskRepo.ReadSingleTaskSeries(ctx, task.SpaceID, task.TaskID)
	if err != nil || !single || series == nil {
		return nil
	}
	seriesKey := domain.CanonicalSeriesKey(series.Provider, series.SourceID, series.MarketType, series.SubjectID, series.SeriesTag)
	if series.SubjectID == "" || series.ProviderSymbol == "" || series.Provider == "" || series.SourceID == "" || series.MarketType == "" || series.SeriesTag == "" ||
		series.SeriesIndex != 0 || series.SeriesKey != seriesKey || strings.TrimSpace(task.SeriesHash) == "" || task.SeriesHash != domain.SeriesSetHash([]string{seriesKey}) {
		return nil
	}
	return series
}

func (s *Service) setCanaryCandidate(entry *pb.TaskResultInventoryEntry, task domain.CollectionTask, params *domain.CollectParams, series *domain.TaskSeries) {
	if entry == nil || params == nil || series == nil {
		return
	}
	entry.CanaryCandidateAvailable = true
	entry.SubjectId = series.SubjectID
	entry.ProviderSymbol = series.ProviderSymbol
	entry.Provider = series.Provider
	entry.SourceId = series.SourceID
	entry.MarketType = series.MarketType
	entry.SeriesTag = series.SeriesTag
	entry.SeriesIndex = series.SeriesIndex
	entry.SeriesHash = task.SeriesHash
	entry.ExpectedCount = 1
	entry.OutputFields = append([]string(nil), params.OutputFields...)
}

func (s *Service) populateLatestCompletedPeriod(ctx context.Context, entry *pb.TaskResultInventoryEntry, task domain.CollectionTask, datasetID, frequency string) error {
	if s == nil || s.persistence == nil {
		return fmt.Errorf("period state persistence is not initialized")
	}
	var latestPeriod time.Time
	var latestStatus string
	if strings.EqualFold(task.DataType, "kline_resample") {
		if s.persistence.PeriodReadiness() == nil {
			return fmt.Errorf("period readiness repository is not initialized")
		}
		latest, err := s.persistence.PeriodReadiness().LatestCompletedPeriod(ctx, task.SpaceID, datasetID, frequency)
		if err != nil {
			return fmt.Errorf("read latest completed resample period: %w", err)
		}
		if latest != nil {
			latestPeriod, latestStatus = latest.PeriodTime, latest.Status
		}
	} else {
		if s.persistence.PeriodStorageStates() == nil {
			return fmt.Errorf("period Storage state repository is not initialized")
		}
		latest, err := s.persistence.PeriodStorageStates().LatestTerminalPeriod(ctx, task.SpaceID, datasetID, frequency)
		if err != nil {
			return fmt.Errorf("read latest Storage-confirmed period: %w", err)
		}
		if latest != nil {
			latestPeriod, latestStatus = latest.Key.PeriodTime, latest.Status
		}
	}
	if !latestPeriod.IsZero() {
		entry.LatestCompletedPeriod = latestPeriod.UTC().Format(time.RFC3339Nano)
		entry.LatestCompletedStatus = latestStatus
	}
	return nil
}

func taskResultMarketCalendar(marketID string) (calendarID, timezone string, sessions []string) {
	switch strings.ToLower(strings.TrimSpace(marketID)) {
	case "crypto":
		return "", "UTC", nil
	case "stockcn":
		return "cn_stock", "Asia/Shanghai", []string{"09:30-11:30", "13:00-15:00"}
	default:
		return "", "", nil
	}
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
