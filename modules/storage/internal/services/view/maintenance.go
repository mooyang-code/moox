package view

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type FactReader interface {
	ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error)
}

type RecordFactReader interface {
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
	ScanRecordRows(ctx context.Context, spaceID string, datasetID string, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error)
}

type RecordReplayReader interface {
	OpenRecordSnapshot(ctx context.Context, req *pb.OpenRecordAccessSnapshotReq) (*pb.OpenRecordAccessSnapshotRsp, error)
	ReadRecordSnapshot(ctx context.Context, req *pb.ReadRecordAccessSnapshotReq) (*pb.ReadRecordAccessSnapshotRsp, error)
	ScanRecordSnapshot(ctx context.Context, req *pb.ScanRecordAccessSnapshotReq) (*pb.ScanRecordAccessSnapshotRsp, error)
	CloseRecordSnapshot(ctx context.Context, snapshotID string) error
	RecordWatermark(ctx context.Context, scope *pb.RecordAccessScope) (sourceID string, commitSeq uint64, err error)
	ScanRecordJournal(ctx context.Context, scope *pb.RecordAccessScope, after, through uint64, page *pb.Page) ([]*pb.RecordRowsCommittedEvent, uint64, *pb.PageResult, error)
}

type ManagedViewIndex interface {
	viewindex.ViewIndexEngine
	List(ctx context.Context) ([]string, error)
}

type MaintenanceConfig struct {
	Enabled                    bool
	OwnerID                    string
	LeaseTTL                   time.Duration
	RunBudget                  time.Duration
	PageSize                   uint32
	MaxEntries                 int64
	TargetEntries              int64
	MaxPhysicalBytes           uint64
	MinFreeDiskBytes           uint64
	MinReadyEntries            int64
	AllowedLag                 time.Duration
	OverlapWindow              time.Duration
	RemoveGrace                time.Duration
	TimeSeriesDefaultRetention time.Duration
	TimeSeriesRetentionByFreq  map[string]time.Duration
	RecordRetention            time.Duration
	MaxPagesPerViewPerRun      int
	MaxViewsPerRun             int
}

type MaintenanceOptions struct {
	Metadata Metadata
	Engines  map[string]ManagedViewIndex
	Facts    FactReader
	Records  RecordFactReader
	Config   MaintenanceConfig
	Now      func() time.Time
}

type MaintenanceManager struct {
	metadata Metadata
	engines  map[string]ManagedViewIndex
	facts    FactReader
	records  RecordFactReader
	cfg      MaintenanceConfig
	now      func() time.Time

	runMu       sync.Mutex
	mu          sync.Mutex
	orphanSince map[string]time.Time
	lastViewKey string
}

func NewMaintenanceManager(opts MaintenanceOptions) *MaintenanceManager {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	cfg := opts.Config
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 90 * time.Second
	}
	if cfg.RunBudget <= 0 {
		cfg.RunBudget = 20 * time.Second
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = 500
	}
	if cfg.MaxPagesPerViewPerRun <= 0 {
		cfg.MaxPagesPerViewPerRun = 32
	}
	if cfg.MaxViewsPerRun <= 0 {
		cfg.MaxViewsPerRun = 2
	}
	if cfg.TimeSeriesDefaultRetention <= 0 {
		cfg.TimeSeriesDefaultRetention = 7 * 24 * time.Hour
	}
	if cfg.RecordRetention <= 0 {
		cfg.RecordRetention = 30 * 24 * time.Hour
	}
	if cfg.OverlapWindow <= 0 {
		cfg.OverlapWindow = 30 * time.Minute
	}
	return &MaintenanceManager{
		metadata: opts.Metadata, engines: opts.Engines, facts: opts.Facts, records: opts.Records,
		cfg: cfg, now: now, orphanSince: make(map[string]time.Time),
	}
}

func (m *MaintenanceManager) MaintainViewIndexes(ctx context.Context, spaceID string) (int, error) {
	if m == nil || m.metadata == nil {
		return 0, errors.New("View index maintenance requires metadata")
	}
	if !m.cfg.Enabled {
		return 0, nil
	}
	if !m.runMu.TryLock() {
		return 0, nil
	}
	defer m.runMu.Unlock()
	views, err := m.listViews(ctx, spaceID)
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(m.cfg.RunBudget)
	changed := 0
	var joined error
	for _, item := range m.viewsForRun(views) {
		if err := ctx.Err(); err != nil {
			return changed, errors.Join(joined, err)
		}
		if time.Now().After(deadline) {
			break
		}
		m.lastViewKey = maintenanceViewKey(item)
		activated, err := m.maintainView(ctx, item, deadline)
		if err != nil {
			joined = errors.Join(joined, fmt.Errorf("maintain View %s/%s: %w", item.GetSpaceId(), item.GetViewId(), err))
			continue
		}
		if activated {
			changed++
		}
	}
	if fresh, err := m.listViews(ctx, spaceID); err == nil {
		m.sweepOrphans(ctx, fresh)
	} else {
		joined = errors.Join(joined, err)
	}
	return changed, joined
}

func (m *MaintenanceManager) viewsForRun(views []*pb.View) []*pb.View {
	if len(views) == 0 || m.cfg.MaxViewsPerRun <= 0 {
		return nil
	}
	limit := m.cfg.MaxViewsPerRun
	if limit > len(views) {
		limit = len(views)
	}
	start := 0
	if m.lastViewKey != "" {
		start = len(views)
		for i, item := range views {
			if maintenanceViewKey(item) > m.lastViewKey {
				start = i
				break
			}
		}
		if start == len(views) {
			start = 0
		}
	}
	out := make([]*pb.View, 0, limit)
	for offset := 0; offset < limit; offset++ {
		out = append(out, views[(start+offset)%len(views)])
	}
	return out
}

func maintenanceViewKey(item *pb.View) string {
	if item == nil {
		return ""
	}
	return item.GetSpaceId() + "\x00" + item.GetViewId()
}

func (m *MaintenanceManager) maintainView(ctx context.Context, item *pb.View, deadline time.Time) (bool, error) {
	if item != nil && strings.EqualFold(item.GetEngine(), "bleve") && item.GetRecordViewMode() == pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
		item = proto.Clone(item).(*pb.View)
		item.RecordViewMode = pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT
	}
	engineKey := strings.ToLower(strings.TrimSpace(item.GetEngine()))
	engine := m.engines[engineKey]
	if engine == nil {
		return false, nil
	}
	build := item.GetIndexBuild()
	if build == nil || build.GetState() == pb.ViewIndexBuild_FAILED || build.GetTargetViewVersion() != item.GetViewVersion() {
		needBuild, err := m.needsBuild(ctx, item, engine)
		if err != nil {
			return false, err
		}
		if build != nil && build.GetTargetViewVersion() != item.GetViewVersion() {
			needBuild = true
		}
		if !needBuild && (build == nil || build.GetState() != pb.ViewIndexBuild_FAILED) {
			return false, nil
		}
		item, build, err = m.startBuild(ctx, item, engineKey, engine)
		if err != nil {
			return false, err
		}
		if build == nil {
			return false, nil
		}
	} else {
		owned, err := m.ensureLease(ctx, item, build)
		if err != nil {
			return false, err
		}
		if !owned {
			return false, nil
		}
		build = item.GetIndexBuild()
	}

	for pageCount := 0; pageCount < m.cfg.MaxPagesPerViewPerRun && !time.Now().After(deadline); pageCount++ {
		var (
			activated bool
			next      *pb.ViewIndexBuild
			err       error
		)
		switch build.GetState() {
		case pb.ViewIndexBuild_PREPARING:
			next, err = m.prepareBuild(ctx, item, build, engine)
		case pb.ViewIndexBuild_BUILDING:
			next, activated, err = m.processBuildPage(ctx, item, build, engine, false)
		case pb.ViewIndexBuild_CATCHING_UP:
			next, activated, err = m.processBuildPage(ctx, item, build, engine, true)
		case pb.ViewIndexBuild_READY:
			_, err = m.metadata.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
				SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
			})
			return err == nil, err
		default:
			return false, nil
		}
		if err != nil {
			_, _ = m.metadata.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
				SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID, Error: err.Error(),
			})
			return false, err
		}
		if activated {
			return true, nil
		}
		if next == nil {
			return false, nil
		}
		build = next
		item.IndexBuild = next
	}
	return false, nil
}

func (m *MaintenanceManager) needsBuild(ctx context.Context, item *pb.View, engine ManagedViewIndex) (bool, error) {
	if item.GetActiveIndexId() == "" || item.GetActiveViewVersion() != item.GetViewVersion() {
		return true, nil
	}
	columns, err := m.viewColumns(ctx, item)
	if err != nil {
		return false, err
	}
	schema := schemaForView(item, columns)
	wantHash := viewindex.HashViewIndexSchema(schema)
	if item.GetActiveSchemaHash() != wantHash {
		return true, nil
	}
	stats, err := engine.Stat(ctx, item.GetActiveIndexId())
	if err != nil {
		return false, err
	}
	if !stats.Exists || stats.ViewVersion != item.GetActiveViewVersion() || stats.SchemaHash != wantHash {
		return true, nil
	}
	window, err := m.retentionWindow(item)
	if err != nil {
		return false, err
	}
	coverageStale := m.coverageStale(item, window)
	if coverageStale {
		return true, nil
	}
	entryPressure := m.cfg.MaxEntries > 0 && stats.EntryCount > m.cfg.MaxEntries
	bytePressure := m.cfg.MaxPhysicalBytes > 0 && stats.PhysicalBytes > m.cfg.MaxPhysicalBytes
	if entryPressure || bytePressure {
		if item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED || strings.EqualFold(item.GetEngine(), "bleve") {
			return true, nil
		}
		return m.pressureCanShrink(stats, window, entryPressure), nil
	}
	return false, nil
}

func (m *MaintenanceManager) pressureCanShrink(stats viewindex.ViewIndexStats, window time.Duration, entryPressure bool) bool {
	minVersion, minOK := parseIndexTime(stats.MinVersion)
	maxVersion, maxOK := parseIndexTime(stats.MaxVersion)
	if !minOK || !maxOK || !maxVersion.After(minVersion) {
		return false
	}
	span := maxVersion.Sub(minVersion)
	if span <= window+m.cfg.AllowedLag {
		return false
	}
	if !entryPressure || m.cfg.TargetEntries <= 0 || stats.EntryCount <= 0 {
		return true
	}
	estimatedRetained := float64(stats.EntryCount) * float64(window) / float64(span)
	return estimatedRetained <= float64(m.cfg.TargetEntries)
}

func (m *MaintenanceManager) coverageStale(item *pb.View, window time.Duration) bool {
	start, ok := parseIndexTime(item.GetActiveCoverageStart())
	if !ok {
		return true
	}
	desired := m.now().UTC().Add(-window)
	slack := window / 10
	if slack < m.cfg.OverlapWindow {
		slack = m.cfg.OverlapWindow
	}
	return start.Before(desired.Add(-slack))
}

func (m *MaintenanceManager) startBuild(ctx context.Context, item *pb.View, engineKey string, engine ManagedViewIndex) (*pb.View, *pb.ViewIndexBuild, error) {
	columns, err := m.viewColumns(ctx, item)
	if err != nil {
		return item, nil, err
	}
	indexID := viewindex.InactiveViewIndexID(item.GetSpaceId(), item.GetViewId(), item.GetActiveIndexId())
	if m.cfg.MinFreeDiskBytes > 0 {
		stats, statErr := engine.Stat(ctx, indexID)
		if statErr != nil {
			return item, nil, fmt.Errorf("inspect owner free disk: %w", statErr)
		}
		if stats.FreeDiskBytes < m.cfg.MinFreeDiskBytes {
			return item, nil, fmt.Errorf("view index owner free disk %d bytes is below required floor %d bytes", stats.FreeDiskBytes, m.cfg.MinFreeDiskBytes)
		}
	}
	schema := schemaForView(item, columns)
	schema.ViewVersion = item.GetViewVersion()
	schema.Engine = engineKey
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	buildID, err := newBuildID()
	if err != nil {
		return item, nil, err
	}
	build, _, err := m.metadata.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(), BuildId: buildID, IndexId: indexID,
		Engine: engineKey, TargetViewVersion: item.GetViewVersion(), OwnerId: m.cfg.OwnerID,
		LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL), SchemaHash: schema.SchemaHash,
		Columns: columns, SnapshotEnd: m.now().UTC().Format(time.RFC3339Nano), ExpectedActiveIndexId: item.GetActiveIndexId(),
	})
	if err != nil {
		return item, nil, err
	}
	copyItem := proto.Clone(item).(*pb.View)
	copyItem.IndexBuild = build
	return copyItem, build, nil
}

func (m *MaintenanceManager) ensureLease(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild) (bool, error) {
	now := m.now().UTC()
	expires, ok := parseIndexTime(build.GetLeaseExpiresAt())
	if build.GetOwnerId() == m.cfg.OwnerID && ok && expires.After(now) {
		return true, nil
	}
	if ok && expires.After(now) {
		return false, nil
	}
	resumed, _, err := m.metadata.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(), BuildId: build.GetBuildId(), IndexId: build.GetIndexId(),
		Engine: build.GetEngine(), TargetViewVersion: build.GetTargetViewVersion(), OwnerId: m.cfg.OwnerID,
		LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL), SchemaHash: build.GetSchemaHash(), Columns: build.GetColumns(),
		SnapshotEnd: build.GetSnapshotEnd(), ExpectedActiveIndexId: item.GetActiveIndexId(),
	})
	if err != nil {
		return false, err
	}
	item.IndexBuild = resumed
	return true, nil
}

func (m *MaintenanceManager) prepareBuild(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, engine ManagedViewIndex) (*pb.ViewIndexBuild, error) {
	preparedSchema := viewindex.ViewIndexSchema{
		SpaceID: build.GetSpaceId(), ViewID: build.GetViewId(), ViewVersion: build.GetTargetViewVersion(),
		Engine: build.GetEngine(), Columns: build.GetColumns(), SchemaHash: build.GetSchemaHash(),
	}
	if item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
		preparedSchema.RecordViewMode = item.GetRecordViewMode()
		preparedSchema.LayoutRevision = viewindex.RecordLayoutRevision
		preparedSchema.PrimaryDatasetID = item.GetPrimaryDatasetId()
		preparedSchema.DatasetIDs = item.GetDatasetIds()
		preparedSchema.GrainKeys = item.GetGrainKeys()
		preparedSchema.FilterJSON = item.GetFilterJson()
	}
	if err := engine.Prepare(ctx, build.GetIndexId(), preparedSchema); err != nil {
		return nil, err
	}
	cursorState := buildCursor{Phase: buildPhaseBackfill}
	update := &pb.UpdateViewIndexBuildReq{SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
		ExpectedState: pb.ViewIndexBuild_PREPARING, NextState: pb.ViewIndexBuild_BUILDING,
		LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL)}
	if item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
		if replay, ok := m.records.(RecordReplayReader); ok {
			datasets := append([]string(nil), item.GetDatasetIds()...)
			if len(datasets) == 0 {
				datasets = []string{item.GetPrimaryDatasetId()}
			}
			openReq := &pb.OpenRecordAccessSnapshotReq{Scope: &pb.RecordAccessScope{SpaceId: item.GetSpaceId(), DatasetIds: datasets}, Mode: pb.RecordReadMode(item.GetRecordViewMode())}
			if item.GetRecordViewMode() == pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY {
				window, windowErr := m.retentionWindow(item)
				if windowErr != nil {
					return nil, windowErr
				}
				if snapshotEnd, parseOK := parseIndexTime(build.GetSnapshotEnd()); parseOK {
					openReq.UpdatedTimeRange = &pb.TimeRange{StartTime: snapshotEnd.Add(-window).Format(time.RFC3339Nano), EndTime: snapshotEnd.Format(time.RFC3339Nano)}
					update.RetentionCutoffAt = openReq.UpdatedTimeRange.GetStartTime()
				}
			}
			opened, openErr := replay.OpenRecordSnapshot(ctx, openReq)
			if openErr != nil {
				return nil, openErr
			}
			cursorState.SnapshotID, cursorState.SnapshotCommitSeq, cursorState.SourceID = opened.GetSnapshotId(), opened.GetSnapshotCommitSeq(), opened.GetSourceId()
			update.SnapshotId, update.SnapshotCommitSeq, update.RecordSourceId = cursorState.SnapshotID, cursorState.SnapshotCommitSeq, cursorState.SourceID
		}
	}
	cursor, err := encodeBuildCursor(cursorState)
	if err != nil {
		return nil, err
	}
	update.CursorJson = cursor
	return m.metadata.UpdateViewIndexBuild(ctx, update)
}

func schemaForView(item *pb.View, columns []*pb.ViewColumn) viewindex.ViewIndexSchema {
	schema := viewindex.ViewIndexSchema{SpaceID: item.GetSpaceId(), ViewID: item.GetViewId(), Engine: item.GetEngine(), Columns: columns}
	if item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
		schema.PrimaryDatasetID = item.GetPrimaryDatasetId()
		schema.DatasetIDs = append([]string(nil), item.GetDatasetIds()...)
		schema.GrainKeys = append([]string(nil), item.GetGrainKeys()...)
		schema.FilterJSON = item.GetFilterJson()
		schema.RecordViewMode = item.GetRecordViewMode()
		schema.LayoutRevision = viewindex.RecordLayoutRevision
	}
	return schema
}

func (m *MaintenanceManager) processBuildPage(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, engine ManagedViewIndex, catchUp bool) (*pb.ViewIndexBuild, bool, error) {
	cursor, err := decodeBuildCursor(build.GetCursorJson())
	if err != nil {
		return nil, false, err
	}
	wantPhase := buildPhaseBackfill
	if catchUp {
		wantPhase = buildPhaseCatchUp
	}
	if cursor.Phase != wantPhase {
		return nil, false, fmt.Errorf("build cursor phase %q does not match state", cursor.Phase)
	}
	window, err := m.retentionWindow(item)
	if err != nil {
		return nil, false, err
	}
	snapshotEnd, ok := parseIndexTime(build.GetSnapshotEnd())
	if !ok {
		return nil, false, errors.New("build snapshot_end must be RFC3339")
	}
	end := snapshotEnd
	start := snapshotEnd.Add(-window)
	if catchUp {
		start = snapshotEnd.Add(-m.catchUpOverlapWindow(item))
		end, ok = parseIndexTime(build.GetCoverageEnd())
		if !ok || end.Before(snapshotEnd) {
			return nil, false, errors.New("catch-up coverage_end must be an RFC3339 time at or after snapshot_end")
		}
	}
	itemForBuild := proto.Clone(item).(*pb.View)
	itemForBuild.Columns = cloneColumns(build.GetColumns())
	var (
		written         int
		page            *pb.PageResult
		nextPage        string
		replayedThrough = cursor.ReplayedCommitSeq
	)
	if catchUp && item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
		if replay, ok := m.records.(RecordReplayReader); ok {
			build, err = m.ensureRecordReplayBoundary(ctx, item, build, replay)
			if err == nil {
				written, page, replayedThrough, err = m.processRecordJournalPage(ctx, itemForBuild, build, engine, cursor.Cursor, replayedThrough)
			}
		} else {
			written, page, err = m.processRecordPage(ctx, itemForBuild, build, engine, cursor.Cursor, start, end, cursor.SnapshotID, cursor.SourceID, cursor.SnapshotCommitSeq)
		}
	} else if strings.EqualFold(build.GetEngine(), "bleve") {
		written, page, err = m.processRecordPage(ctx, itemForBuild, build, engine, cursor.Cursor, start, end, cursor.SnapshotID, cursor.SourceID, cursor.SnapshotCommitSeq)
	} else {
		written, page, err = m.processTimeSeriesPage(ctx, itemForBuild, build, engine, cursor.Cursor, start, end)
	}
	if err != nil {
		return nil, false, err
	}
	if page != nil && page.GetHasMore() && page.GetNextCursor() != "" {
		nextPage = page.GetNextCursor()
		nextCursor, err := encodeBuildCursor(buildCursor{Phase: wantPhase, Cursor: nextPage, SnapshotID: cursor.SnapshotID, SnapshotCommitSeq: cursor.SnapshotCommitSeq, SourceID: cursor.SourceID, ReplayedCommitSeq: replayedThrough})
		if err != nil {
			return nil, false, err
		}
		update := &pb.UpdateViewIndexBuildReq{
			SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
			ExpectedState: build.GetState(), NextState: build.GetState(), LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL),
			CursorJson: nextCursor, EntriesWritten: build.GetEntriesWritten() + uint64(written),
		}
		if catchUp && item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
			update.ReplayedCommitSeq = replayedThrough
		}
		next, err := m.metadata.UpdateViewIndexBuild(ctx, update)
		return next, false, err
	}
	if !catchUp {
		if cursor.SnapshotID != "" {
			if replay, ok := m.records.(RecordReplayReader); ok {
				_ = replay.CloseRecordSnapshot(ctx, cursor.SnapshotID)
			}
		}
		catchUpEnd := m.now().UTC()
		if catchUpEnd.Before(snapshotEnd) {
			catchUpEnd = snapshotEnd
		}
		replayedCommitSeq := uint64(0)
		if item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
			replayedCommitSeq = cursor.SnapshotCommitSeq
		}
		nextCursor, err := encodeBuildCursor(buildCursor{Phase: buildPhaseCatchUp, ReplayedCommitSeq: replayedCommitSeq})
		if err != nil {
			return nil, false, err
		}
		update := &pb.UpdateViewIndexBuildReq{
			SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
			ExpectedState: pb.ViewIndexBuild_BUILDING, NextState: pb.ViewIndexBuild_CATCHING_UP,
			LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL), CursorJson: nextCursor,
			CoverageStart: start.Format(time.RFC3339Nano), CoverageEnd: catchUpEnd.Format(time.RFC3339Nano),
			EntriesWritten: build.GetEntriesWritten() + uint64(written),
		}
		if item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
			update.ReplayedCommitSeq = replayedCommitSeq
			update.SnapshotId = cursor.SnapshotID
			update.SnapshotCommitSeq = cursor.SnapshotCommitSeq
			update.RecordSourceId = cursor.SourceID
		}
		next, err := m.metadata.UpdateViewIndexBuild(ctx, update)
		return next, false, err
	}
	stats, err := engine.Stat(ctx, build.GetIndexId())
	if err != nil {
		return nil, false, err
	}
	ready, err := m.buildReady(ctx, item, build, engine, stats)
	if err != nil || !ready {
		return nil, false, err
	}
	update := &pb.UpdateViewIndexBuildReq{
		SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
		ExpectedState: pb.ViewIndexBuild_CATCHING_UP, NextState: pb.ViewIndexBuild_READY,
		LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL),
		CoverageStart:   snapshotEnd.Add(-window).Format(time.RFC3339Nano), CoverageEnd: end.Format(time.RFC3339Nano),
		EntriesWritten: build.GetEntriesWritten() + uint64(written),
	}
	if item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
		update.ReplayedCommitSeq = replayedThrough
	}
	readyBuild, err := m.metadata.UpdateViewIndexBuild(ctx, update)
	if err != nil {
		return nil, false, err
	}
	_, err = m.metadata.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(), BuildId: readyBuild.GetBuildId(), OwnerId: m.cfg.OwnerID,
	})
	return nil, err == nil, err
}

func (m *MaintenanceManager) catchUpOverlapWindow(item *pb.View) time.Duration {
	overlap := m.cfg.OverlapWindow
	if !strings.EqualFold(item.GetEngine(), "duckdb") {
		return overlap
	}
	filter, err := parseFixedViewFilter(item.GetFilterJson())
	if err != nil || filter == nil {
		return overlap
	}
	if frequency, ok := parseFrequencyDuration(filter.Freq); ok && frequency > overlap {
		return frequency
	}
	return overlap
}

func (m *MaintenanceManager) processTimeSeriesPage(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, engine ManagedViewIndex, cursor string, start time.Time, end time.Time) (int, *pb.PageResult, error) {
	if m.facts == nil {
		return 0, nil, errors.New("time series View maintenance requires a FactReader")
	}
	datasetID := item.GetPrimaryDatasetId()
	rows, page, err := m.facts.ScanTimeSeriesRows(ctx, item.GetSpaceId(), datasetID, &pb.TimeRange{
		StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano),
	}, sourceColumns(datasetID, build.GetColumns()), &pb.Page{Size: m.cfg.PageSize, Cursor: cursor})
	if err != nil {
		return 0, nil, err
	}
	projected, ok, err := FilteredTimeSeriesRowsForView(ctx, item, build.GetColumns(), rows, m.readTimeSeriesProjectionRows)
	if err != nil {
		return 0, nil, err
	}
	if !ok {
		return 0, nil, errors.New("time series View contains unsupported projection columns")
	}
	if len(projected) > 0 {
		if err := engine.Write(ctx, build.GetIndexId(), viewindex.ViewIndexBatch{TimeSeriesRows: projected, Columns: build.GetColumns(), ViewVersion: build.GetTargetViewVersion(), SchemaHash: build.GetSchemaHash()}); err != nil {
			return 0, nil, err
		}
	}
	return len(projected), page, nil
}

func recordViewScope(item *pb.View) *pb.RecordAccessScope {
	datasetIDs := append([]string(nil), item.GetDatasetIds()...)
	if len(datasetIDs) == 0 && item.GetPrimaryDatasetId() != "" {
		datasetIDs = []string{item.GetPrimaryDatasetId()}
	}
	return &pb.RecordAccessScope{SpaceId: item.GetSpaceId(), DatasetIds: datasetIDs}
}

func (m *MaintenanceManager) ensureRecordReplayBoundary(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, replay RecordReplayReader) (*pb.ViewIndexBuild, error) {
	if build.GetReplayThroughCommitSeq() > 0 && build.GetRecordSourceId() != "" {
		return build, nil
	}
	sourceID, through, err := replay.RecordWatermark(ctx, recordViewScope(item))
	if err != nil {
		return nil, err
	}
	if build.GetSnapshotCommitSeq() > through {
		return nil, fmt.Errorf("Record replay watermark %d is behind snapshot commit %d", through, build.GetSnapshotCommitSeq())
	}
	return m.metadata.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
		SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
		ExpectedState: build.GetState(), NextState: build.GetState(), LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL),
		ReplayThroughCommitSeq: through, ReplayedCommitSeq: build.GetSnapshotCommitSeq(), RecordSourceId: sourceID,
	})
}

func (m *MaintenanceManager) processRecordJournalPage(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, engine ManagedViewIndex, cursor string, after uint64) (int, *pb.PageResult, uint64, error) {
	replay, ok := m.records.(RecordReplayReader)
	if !ok {
		return 0, nil, after, errors.New("Record replay reader is required for journal catch-up")
	}
	through := build.GetReplayThroughCommitSeq()
	events, scannedThrough, page, err := replay.ScanRecordJournal(ctx, recordViewScope(item), after, through, &pb.Page{Size: m.cfg.PageSize, Cursor: cursor})
	if err != nil {
		return 0, nil, after, err
	}
	if scannedThrough < after || scannedThrough > through {
		return 0, nil, after, fmt.Errorf("Record journal scanned cursor %d is outside (%d,%d]", scannedThrough, after, through)
	}
	mutations, err := m.recordJournalMutations(ctx, item, events, replay)
	if err != nil {
		return 0, nil, after, err
	}
	if len(mutations) > 0 || scannedThrough > after {
		batch := viewindex.ViewIndexBatch{
			RecordMutations: mutations, RecordViewMode: item.GetRecordViewMode(), Columns: item.GetColumns(),
			ViewVersion: build.GetTargetViewVersion(), SchemaHash: build.GetSchemaHash(), ReplaySourceID: build.GetRecordSourceId(),
			ReplayFromCommitSeq: after, ReplayThroughCommitSeq: scannedThrough,
		}
		if err := engine.Write(ctx, build.GetIndexId(), batch); err != nil {
			return 0, nil, after, err
		}
	}
	return len(mutations), page, scannedThrough, nil
}

func (m *MaintenanceManager) recordJournalMutations(ctx context.Context, item *pb.View, events []*pb.RecordRowsCommittedEvent, replay RecordReplayReader) ([]*pb.RecordIndexMutation, error) {
	if item.GetRecordViewMode() == pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY {
		out := make([]*pb.RecordIndexMutation, 0)
		for _, event := range events {
			if event == nil {
				continue
			}
			for _, row := range event.GetRows() {
				if row == nil || row.GetKey() == nil || row.GetKey().GetDatasetId() != item.GetPrimaryDatasetId() {
					continue
				}
				mutation, err := BuildHistoryRecordMutation(item, row, event.GetSourceId())
				if err != nil {
					return nil, err
				}
				mutation.OrderCommitSeq = event.GetCommitSeq()
				out = append(out, mutation)
			}
		}
		return out, nil
	}
	ids := make([]string, 0)
	seenIDs := make(map[string]struct{})
	for _, event := range events {
		for _, row := range event.GetRows() {
			if row == nil || row.GetKey() == nil || row.GetKey().GetDatasetId() != item.GetPrimaryDatasetId() {
				continue
			}
			if _, seen := seenIDs[row.GetKey().GetRecordId()]; !seen {
				seenIDs[row.GetKey().GetRecordId()] = struct{}{}
				ids = append(ids, row.GetKey().GetRecordId())
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	opened, err := replay.OpenRecordSnapshot(ctx, &pb.OpenRecordAccessSnapshotReq{Scope: recordViewScope(item), Mode: pb.RecordReadMode_RECORD_READ_MODE_CURRENT})
	if err != nil {
		return nil, err
	}
	defer func() { _ = replay.CloseRecordSnapshot(ctx, opened.GetSnapshotId()) }()
	rowsByDataset := make(map[string]map[string]*pb.RecordRow)
	for _, datasetID := range ViewProjectionDatasets(item.GetPrimaryDatasetId(), item.GetColumns()) {
		read, readErr := replay.ReadRecordSnapshot(ctx, &pb.ReadRecordAccessSnapshotReq{SnapshotId: opened.GetSnapshotId(), DatasetId: datasetID, RecordIds: ids})
		if readErr != nil {
			return nil, readErr
		}
		if read == nil || read.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			if read == nil || read.GetRetInfo() == nil {
				return nil, errors.New("Record journal snapshot read returned an empty response")
			}
			return nil, errors.New(read.GetRetInfo().GetMsg())
		}
		byID := make(map[string]*pb.RecordRow, len(read.GetRows()))
		for _, row := range read.GetRows() {
			if row != nil && row.GetKey() != nil {
				byID[row.GetKey().GetRecordId()] = row
			}
		}
		rowsByDataset[datasetID] = byID
	}
	out := make([]*pb.RecordIndexMutation, 0, len(ids))
	for _, recordID := range ids {
		rows := make(map[string]*pb.RecordRow, len(rowsByDataset))
		for datasetID, byID := range rowsByDataset {
			if row := byID[recordID]; row != nil {
				rows[datasetID] = row
			}
		}
		mutation, err := BuildCurrentRecordMutation(item, rows, opened.GetSourceId(), opened.GetSnapshotCommitSeq())
		if err != nil {
			return nil, err
		}
		if mutation != nil {
			out = append(out, mutation)
		}
	}
	return out, nil
}

func (m *MaintenanceManager) processRecordPage(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, engine ManagedViewIndex, cursor string, start time.Time, end time.Time, snapshotID string, sourceID string, snapshotCommitSeq uint64) (int, *pb.PageResult, error) {
	if m.records == nil {
		return 0, nil, errors.New("Record View maintenance requires a RecordFactReader")
	}
	datasetID := item.GetPrimaryDatasetId()
	if item.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_UNSPECIFIED {
		if replay, ok := m.records.(RecordReplayReader); ok {
			if snapshotID == "" {
				openReq := &pb.OpenRecordAccessSnapshotReq{Scope: &pb.RecordAccessScope{SpaceId: item.GetSpaceId(), DatasetIds: []string{datasetID}}, Mode: pb.RecordReadMode(item.GetRecordViewMode())}
				if item.GetRecordViewMode() == pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY {
					openReq.UpdatedTimeRange = &pb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)}
				}
				opened, err := replay.OpenRecordSnapshot(ctx, openReq)
				if err != nil {
					return 0, nil, err
				}
				snapshotID, sourceID, snapshotCommitSeq = opened.GetSnapshotId(), opened.GetSourceId(), opened.GetSnapshotCommitSeq()
				defer func() { _ = replay.CloseRecordSnapshot(ctx, snapshotID) }()
			}
			pageRsp, scanErr := replay.ScanRecordSnapshot(ctx, &pb.ScanRecordAccessSnapshotReq{SnapshotId: snapshotID, DatasetId: datasetID, Page: &pb.Page{Size: m.cfg.PageSize, Cursor: cursor}})
			if scanErr != nil {
				return 0, nil, scanErr
			}
			mutations := make([]*pb.RecordIndexMutation, 0, len(pageRsp.GetRows()))
			secondaryRows := make(map[string]map[string]*pb.RecordRow)
			if item.GetRecordViewMode() == pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT {
				recordIDs := make([]string, 0, len(pageRsp.GetRows()))
				seenRecordIDs := make(map[string]struct{}, len(pageRsp.GetRows()))
				for _, row := range pageRsp.GetRows() {
					if row == nil || row.GetKey() == nil || row.GetKey().GetRecordId() == "" {
						continue
					}
					if _, seen := seenRecordIDs[row.GetKey().GetRecordId()]; !seen {
						seenRecordIDs[row.GetKey().GetRecordId()] = struct{}{}
						recordIDs = append(recordIDs, row.GetKey().GetRecordId())
					}
				}
				for _, secondaryDatasetID := range ViewProjectionDatasets(datasetID, item.GetColumns()) {
					if secondaryDatasetID == datasetID || len(recordIDs) == 0 {
						continue
					}
					read, readErr := replay.ReadRecordSnapshot(ctx, &pb.ReadRecordAccessSnapshotReq{SnapshotId: snapshotID, DatasetId: secondaryDatasetID, RecordIds: recordIDs})
					if readErr != nil {
						return 0, nil, readErr
					}
					if read == nil || read.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
						if read == nil || read.GetRetInfo() == nil {
							return 0, nil, errors.New("Record snapshot projection read returned an empty response")
						}
						return 0, nil, errors.New(read.GetRetInfo().GetMsg())
					}
					byRecordID := make(map[string]*pb.RecordRow, len(read.GetRows()))
					for _, row := range read.GetRows() {
						if row != nil && row.GetKey() != nil {
							byRecordID[row.GetKey().GetRecordId()] = row
						}
					}
					secondaryRows[secondaryDatasetID] = byRecordID
				}
			}
			for _, row := range pageRsp.GetRows() {
				var mutation *pb.RecordIndexMutation
				var err error
				if item.GetRecordViewMode() == pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY {
					mutation, err = BuildHistoryRecordMutation(item, row, sourceID)
					if mutation != nil && snapshotCommitSeq > 0 {
						// The backfill boundary, rather than an individual row's
						// commit, is the replay fence for the whole snapshot.
						mutation.OrderCommitSeq = snapshotCommitSeq
					}
				} else {
					rowsByDataset := map[string]*pb.RecordRow{datasetID: row}
					if row != nil && row.GetKey() != nil {
						for secondaryDatasetID, rowsByRecordID := range secondaryRows {
							if secondary := rowsByRecordID[row.GetKey().GetRecordId()]; secondary != nil {
								rowsByDataset[secondaryDatasetID] = secondary
							}
						}
					}
					mutation, err = BuildCurrentRecordMutation(item, rowsByDataset, sourceID, snapshotCommitSeq)
				}
				if err != nil {
					return 0, nil, err
				}
				if mutation != nil {
					mutations = append(mutations, mutation)
				}
			}
			if len(mutations) > 0 {
				if err := engine.Write(ctx, build.GetIndexId(), viewindex.ViewIndexBatch{RecordMutations: mutations, RecordViewMode: item.GetRecordViewMode(), Columns: build.GetColumns(), ViewVersion: build.GetTargetViewVersion(), SchemaHash: build.GetSchemaHash()}); err != nil {
					return 0, nil, err
				}
			}
			return len(mutations), pageRsp.GetPageResult(), nil
		}
	}
	rows, page, err := m.records.ScanRecordRows(ctx, item.GetSpaceId(), datasetID, sourceColumns(datasetID, build.GetColumns()), &pb.Page{Size: m.cfg.PageSize, Cursor: cursor})
	if err != nil {
		return 0, nil, err
	}
	projected, ok, err := RecordRowsForView(ctx, item, build.GetColumns(), rows, m.readRecordProjectionRows)
	if err != nil {
		return 0, nil, err
	}
	if !ok {
		return 0, nil, errors.New("Record View contains unsupported projection columns")
	}
	if len(projected) > 0 {
		if err := engine.Write(ctx, build.GetIndexId(), viewindex.ViewIndexBatch{RecordRows: projected, Columns: build.GetColumns(), ViewVersion: build.GetTargetViewVersion(), SchemaHash: build.GetSchemaHash()}); err != nil {
			return 0, nil, err
		}
	}
	return len(projected), page, nil
}

func (m *MaintenanceManager) buildReady(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, engine ManagedViewIndex, stats viewindex.ViewIndexStats) (bool, error) {
	if !stats.Exists || stats.ViewVersion != build.GetTargetViewVersion() || stats.SchemaHash != build.GetSchemaHash() {
		return false, nil
	}
	threshold := m.cfg.MinReadyEntries
	if item.GetActiveIndexId() == "" {
		threshold = 0
	} else if active, err := engine.Stat(ctx, item.GetActiveIndexId()); err == nil && active.Exists {
		if active.EntryCount < threshold {
			threshold = active.EntryCount
		}
		if m.cfg.AllowedLag > 0 {
			warmingMax, warmingOK := parseIndexTime(stats.MaxVersion)
			activeMax, activeOK := parseIndexTime(active.MaxVersion)
			if warmingOK && activeOK && activeMax.Sub(warmingMax) > m.cfg.AllowedLag {
				return false, nil
			}
		}
	}
	return stats.EntryCount >= threshold, nil
}

func (m *MaintenanceManager) retentionWindow(item *pb.View) (time.Duration, error) {
	if raw := strings.TrimSpace(item.GetRetentionWindow()); raw != "" {
		explicit, ok := parseDurationWindow(raw)
		if !ok {
			return 0, fmt.Errorf("invalid View retention_window %q", raw)
		}
		return explicit, nil
	}
	if strings.EqualFold(item.GetEngine(), "bleve") {
		return m.cfg.RecordRetention, nil
	}
	filter, err := parseFixedViewFilter(item.GetFilterJson())
	if err != nil {
		return 0, err
	}
	if filter != nil {
		if window := m.cfg.TimeSeriesRetentionByFreq[strings.TrimSpace(filter.Freq)]; window > 0 {
			return window, nil
		}
	}
	return m.cfg.TimeSeriesDefaultRetention, nil
}

func (m *MaintenanceManager) viewColumns(ctx context.Context, item *pb.View) ([]*pb.ViewColumn, error) {
	if len(item.GetColumns()) > 0 {
		return cloneColumns(item.GetColumns()), nil
	}
	columns, _, err := m.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Page: 1, Size: 10000})
	return columns, err
}

func (m *MaintenanceManager) readTimeSeriesProjectionRows(ctx context.Context, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rsp, err := m.facts.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: keys})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errors.New(rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetRows(), nil
}

func (m *MaintenanceManager) readRecordProjectionRows(ctx context.Context, keys []*pb.RecordKey) ([]*pb.RecordRow, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rsp, err := m.records.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: keys})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errors.New(rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetRows(), nil
}

func (m *MaintenanceManager) listViews(ctx context.Context, spaceID string) ([]*pb.View, error) {
	if spaceID != "" {
		return m.listViewsInSpace(ctx, spaceID)
	}
	spaces, _, err := m.metadata.ListSpaces(ctx, "", &pb.Page{Page: 1, Size: 10000})
	if err != nil {
		return nil, err
	}
	var out []*pb.View
	for _, space := range spaces {
		views, err := m.listViewsInSpace(ctx, space.GetSpaceId())
		if err != nil {
			return nil, err
		}
		out = append(out, views...)
	}
	return out, nil
}

func (m *MaintenanceManager) listViewsInSpace(ctx context.Context, spaceID string) ([]*pb.View, error) {
	var out []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		views, page, err := m.metadata.ListViews(ctx, spaceID, "", "active", &pb.Page{Page: pageNo, Size: 1000})
		if err != nil {
			return nil, err
		}
		out = append(out, views...)
		if page == nil || !page.GetHasMore() {
			break
		}
	}
	return out, nil
}

func (m *MaintenanceManager) sweepOrphans(ctx context.Context, views []*pb.View) {
	referenced := make(map[string]bool)
	for _, item := range views {
		engineKey := strings.ToLower(strings.TrimSpace(item.GetEngine()))
		if item.GetActiveIndexId() != "" {
			referenced[engineIndexKey(engineKey, item.GetActiveIndexId())] = true
		}
		if item.GetIndexBuild().GetIndexId() != "" {
			referenced[engineIndexKey(engineKey, item.GetIndexBuild().GetIndexId())] = true
		}
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, engine := range m.engines {
		ids, err := engine.List(ctx)
		if err != nil {
			continue
		}
		for _, indexID := range ids {
			key := engineIndexKey(engine.Engine(), indexID)
			if referenced[key] {
				delete(m.orphanSince, key)
				continue
			}
			since, ok := m.orphanSince[key]
			if !ok {
				m.orphanSince[key] = now
				since = now
			}
			if now.Sub(since) >= m.cfg.RemoveGrace {
				// The initial metadata snapshot can race with a builder claiming this
				// slot. Re-read the affected space immediately before deletion and
				// fail closed when metadata is unavailable.
				referencedNow, err := m.indexReferencedNow(ctx, engine.Engine(), indexID)
				if err != nil || referencedNow {
					if referencedNow {
						delete(m.orphanSince, key)
					}
					continue
				}
				if engine.Remove(ctx, indexID) == nil {
					delete(m.orphanSince, key)
				}
			}
		}
	}
}

func (m *MaintenanceManager) indexReferencedNow(ctx context.Context, engineName string, indexID string) (bool, error) {
	ref, err := viewindex.ParseViewIndexID(indexID)
	if err != nil {
		return false, err
	}
	views, err := m.listViews(ctx, ref.SpaceID)
	if err != nil {
		return false, err
	}
	for _, item := range views {
		if !strings.EqualFold(strings.TrimSpace(item.GetEngine()), strings.TrimSpace(engineName)) {
			continue
		}
		if item.GetActiveIndexId() == indexID || item.GetIndexBuild().GetIndexId() == indexID {
			return true, nil
		}
	}
	return false, nil
}

func engineIndexKey(engine string, indexID string) string {
	return strings.ToLower(strings.TrimSpace(engine)) + "\x00" + indexID
}

func sourceColumns(primaryDatasetID string, columns []*pb.ViewColumn) []string {
	seen := make(map[string]bool)
	var out []string
	for _, column := range columns {
		if ViewColumnOriginDataset(primaryDatasetID, column) != primaryDatasetID {
			continue
		}
		name := ViewColumnSourceName(primaryDatasetID, column)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func cloneColumns(columns []*pb.ViewColumn) []*pb.ViewColumn {
	out := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		if column != nil {
			out = append(out, proto.Clone(column).(*pb.ViewColumn))
		}
	}
	return out
}

func parseDurationWindow(value string) (time.Duration, bool) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return 0, false
	}
	multiplier := time.Duration(1)
	if strings.HasSuffix(value, "d") {
		multiplier = 24 * time.Hour
		value = strings.TrimSuffix(value, "d")
	} else {
		duration, err := time.ParseDuration(value)
		return duration, err == nil && duration > 0
	}
	var count int64
	if _, err := fmt.Sscan(value, &count); err != nil || count <= 0 {
		return 0, false
	}
	return time.Duration(count) * multiplier, true
}

func parseFrequencyDuration(value string) (time.Duration, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return 0, false
	}
	unit := value[len(value)-1:]
	if unit == "w" {
		value = strings.TrimSuffix(value, "w")
	} else if unit == "d" || unit == "h" || unit == "m" || unit == "s" {
		value = strings.TrimSuffix(value, unit)
	} else {
		return 0, false
	}
	count, err := strconv.ParseInt(value, 10, 64)
	if err != nil || count <= 0 {
		return 0, false
	}
	multiplier := map[string]time.Duration{
		"s": time.Second,
		"m": time.Minute,
		"h": time.Hour,
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
	}[unit]
	return time.Duration(count) * multiplier, true
}

func parseIndexTime(value string) (time.Time, bool) {
	value = factkey.NormalizeVersion(strings.TrimSpace(value))
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func durationSeconds(value time.Duration) uint32 {
	seconds := value / time.Second
	if seconds <= 0 {
		return 1
	}
	if seconds > time.Duration(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(seconds)
}

func newBuildID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
