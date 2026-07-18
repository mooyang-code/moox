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

	"github.com/mooyang-code/moox/modules/storage/internal/rowkey"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type FactReader interface {
	ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error)
}

type RecordFactReader interface {
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
	ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error)
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
			next, err = m.prepareBuild(ctx, build, engine)
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
	schema := viewindex.ViewIndexSchema{SpaceID: item.GetSpaceId(), ViewID: item.GetViewId(), Engine: item.GetEngine(), Columns: columns}
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
	start, ok := parseIndexTime(item.GetIndexedFrom())
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
	schema := viewindex.ViewIndexSchema{
		SpaceID: item.GetSpaceId(), ViewID: item.GetViewId(), ViewVersion: item.GetViewVersion(), Engine: engineKey, Columns: columns,
	}
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

func (m *MaintenanceManager) prepareBuild(ctx context.Context, build *pb.ViewIndexBuild, engine ManagedViewIndex) (*pb.ViewIndexBuild, error) {
	if err := engine.Prepare(ctx, build.GetIndexId(), viewindex.ViewIndexSchema{
		SpaceID: build.GetSpaceId(), ViewID: build.GetViewId(), ViewVersion: build.GetTargetViewVersion(),
		Engine: build.GetEngine(), Columns: build.GetColumns(), SchemaHash: build.GetSchemaHash(),
	}); err != nil {
		return nil, err
	}
	cursor, err := encodeBuildCursor(buildCursor{Phase: buildPhaseBackfill})
	if err != nil {
		return nil, err
	}
	return m.metadata.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
		SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
		ExpectedState: pb.ViewIndexBuild_PREPARING, NextState: pb.ViewIndexBuild_BUILDING,
		LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL), CursorJson: cursor,
	})
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
		written  int
		page     *pb.PageResult
		nextPage string
	)
	if strings.EqualFold(build.GetEngine(), "bleve") {
		written, page, err = m.processRecordPage(ctx, itemForBuild, build, engine, cursor.Cursor, start, end)
	} else {
		written, page, err = m.processTimeSeriesPage(ctx, itemForBuild, build, engine, cursor.Cursor, start, end)
	}
	if err != nil {
		return nil, false, err
	}
	if page != nil && page.GetHasMore() && page.GetNextCursor() != "" {
		nextPage = page.GetNextCursor()
		nextCursor, err := encodeBuildCursor(buildCursor{Phase: wantPhase, Cursor: nextPage})
		if err != nil {
			return nil, false, err
		}
		next, err := m.metadata.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
			SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
			ExpectedState: build.GetState(), NextState: build.GetState(), LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL),
			CursorJson: nextCursor, EntriesWritten: build.GetEntriesWritten() + uint64(written),
		})
		return next, false, err
	}
	if !catchUp {
		catchUpEnd := m.now().UTC()
		if catchUpEnd.Before(snapshotEnd) {
			catchUpEnd = snapshotEnd
		}
		nextCursor, err := encodeBuildCursor(buildCursor{Phase: buildPhaseCatchUp})
		if err != nil {
			return nil, false, err
		}
		next, err := m.metadata.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
			SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
			ExpectedState: pb.ViewIndexBuild_BUILDING, NextState: pb.ViewIndexBuild_CATCHING_UP,
			LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL), CursorJson: nextCursor,
			CoverageStart: start.Format(time.RFC3339Nano), CoverageEnd: catchUpEnd.Format(time.RFC3339Nano),
			EntriesWritten: build.GetEntriesWritten() + uint64(written),
		})
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
	readyBuild, err := m.metadata.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
		SpaceId: build.GetSpaceId(), ViewId: build.GetViewId(), BuildId: build.GetBuildId(), OwnerId: m.cfg.OwnerID,
		ExpectedState: pb.ViewIndexBuild_CATCHING_UP, NextState: pb.ViewIndexBuild_READY,
		LeaseTtlSeconds: durationSeconds(m.cfg.LeaseTTL),
		CoverageStart:   snapshotEnd.Add(-window).Format(time.RFC3339Nano), CoverageEnd: end.Format(time.RFC3339Nano),
		EntriesWritten: build.GetEntriesWritten() + uint64(written),
	})
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
	projected, ok, err := FilteredTimeSeriesRowsForView(ctx, item, build.GetColumns(), rows, m.readTimeSeriesRowMapperRows)
	if err != nil {
		return 0, nil, err
	}
	if !ok {
		return 0, nil, errors.New("time series View contains unsupported projection columns")
	}
	if len(projected) > 0 {
		indexedFrom, indexedTo := start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)
		if err := applyMaintenanceRows(ctx, engine, build.GetIndexId(), viewindex.ViewIndexApplyBatch{
			ViewVersion: build.GetTargetViewVersion(), ViewSchemaHash: build.GetSchemaHash(),
			RowWrites: timeSeriesReplaceWrites(projected), CheckpointUpdates: checkpointUpdates(projected),
			IndexRangeUpdate: &viewindex.IndexRangeUpdate{IndexedFrom: &indexedFrom, IndexedTo: &indexedTo},
		}); err != nil {
			return 0, nil, err
		}
	}
	return len(projected), page, nil
}

func (m *MaintenanceManager) processRecordPage(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, engine ManagedViewIndex, cursor string, start time.Time, end time.Time) (int, *pb.PageResult, error) {
	if m.records == nil {
		return 0, nil, errors.New("Record View maintenance requires a RecordFactReader")
	}
	datasetID := item.GetPrimaryDatasetId()
	rows, page, err := m.records.ScanRecordRows(ctx, item.GetSpaceId(), datasetID, &pb.VersionRange{
		StartVersion: start.Format(time.RFC3339Nano), EndVersion: end.Format(time.RFC3339Nano),
	}, sourceColumns(datasetID, build.GetColumns()), &pb.Page{Size: m.cfg.PageSize, Cursor: cursor})
	if err != nil {
		return 0, nil, err
	}
	for _, row := range rows {
		if _, ok := parseIndexTime(row.GetKey().GetVersion()); !ok {
			return 0, nil, fmt.Errorf("Record View %s/%s requires RFC3339 versions; got %q", item.GetSpaceId(), item.GetViewId(), row.GetKey().GetVersion())
		}
	}
	projected, ok, err := RecordRowsForView(ctx, item, build.GetColumns(), rows, m.readRecordRowMapperRows)
	if err != nil {
		return 0, nil, err
	}
	if !ok {
		return 0, nil, errors.New("Record View contains unsupported projection columns")
	}
	if len(projected) > 0 {
		indexedFrom, indexedTo := start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)
		if err := applyMaintenanceRows(ctx, engine, build.GetIndexId(), viewindex.ViewIndexApplyBatch{
			ViewVersion: build.GetTargetViewVersion(), ViewSchemaHash: build.GetSchemaHash(),
			RowWrites: recordReplaceWrites(projected), CheckpointUpdates: checkpointUpdates(projected),
			IndexRangeUpdate: &viewindex.IndexRangeUpdate{IndexedFrom: &indexedFrom, IndexedTo: &indexedTo},
		}); err != nil {
			return 0, nil, err
		}
	}
	return len(projected), page, nil
}

func applyMaintenanceRows(ctx context.Context, engine ManagedViewIndex, indexID string, batch viewindex.ViewIndexApplyBatch) error {
	applier, ok := engine.(viewindex.ViewIndexApplier)
	if !ok {
		return errors.New("view maintenance engine does not support atomic apply")
	}
	if len(batch.CheckpointUpdates) > 0 {
		stats, err := engine.Stat(ctx, indexID)
		if err != nil {
			return err
		}
		for index := range batch.CheckpointUpdates {
			batch.CheckpointUpdates[index].ExpectedLastAppliedSequence = stats.ShardCheckpoints[batch.CheckpointUpdates[index].ShardID]
		}
	}
	return applier.Apply(ctx, indexID, batch)
}

func timeSeriesReplaceWrites(rows []*pb.TimeSeriesRow) []viewindex.RowWrite {
	writes := make([]viewindex.RowWrite, 0, len(rows))
	for _, row := range rows {
		if row != nil && row.GetKey() != nil {
			writes = append(writes, viewindex.RowWrite{Operation: viewindex.RowWriteOperationReplace, Key: viewindex.RowKey{TimeSeriesKey: row.GetKey()}, Columns: row.GetColumns(), Attributes: row.GetAttributes(), SourceShardID: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()})
		}
	}
	return writes
}

func recordReplaceWrites(rows []*pb.RecordRow) []viewindex.RowWrite {
	writes := make([]viewindex.RowWrite, 0, len(rows))
	for _, row := range rows {
		if row != nil && row.GetKey() != nil {
			writes = append(writes, viewindex.RowWrite{Operation: viewindex.RowWriteOperationReplace, Key: viewindex.RowKey{RecordKey: row.GetKey()}, Columns: row.GetColumns(), Attributes: row.GetAttributes(), SourceShardID: row.GetSourceShardId(), SourceSequence: row.GetSourceSequence()})
		}
	}
	return writes
}

func checkpointUpdates(rows any) []viewindex.ShardCheckpointUpdate {
	maxima := make(map[string]uint64)
	add := func(shard string, sequence uint64) {
		if strings.TrimSpace(shard) == "" || sequence == 0 || sequence > maxima[shard] {
			if strings.TrimSpace(shard) != "" && sequence > 0 {
				maxima[shard] = sequence
			}
		}
	}
	switch values := rows.(type) {
	case []*pb.TimeSeriesRow:
		for _, row := range values {
			if row != nil {
				add(row.GetSourceShardId(), row.GetSourceSequence())
			}
		}
	case []*pb.RecordRow:
		for _, row := range values {
			if row != nil {
				add(row.GetSourceShardId(), row.GetSourceSequence())
			}
		}
	}
	updates := make([]viewindex.ShardCheckpointUpdate, 0, len(maxima))
	for shard, sequence := range maxima {
		updates = append(updates, viewindex.ShardCheckpointUpdate{ShardID: shard, LastAppliedSequence: sequence})
	}
	return updates
}

func (m *MaintenanceManager) buildReady(ctx context.Context, item *pb.View, build *pb.ViewIndexBuild, engine ManagedViewIndex, stats viewindex.ViewIndexStats) (bool, error) {
	if !stats.Exists || stats.ViewVersion != build.GetTargetViewVersion() || stats.SchemaHash != build.GetSchemaHash() {
		return false, nil
	}
	coverageEnd, coverageOK := parseIndexTime(build.GetCoverageEnd())
	indexedTo, indexedOK := parseIndexTime(stats.IndexedTo)
	if coverageOK && indexedOK && indexedTo.Before(coverageEnd) {
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

func (m *MaintenanceManager) readTimeSeriesRowMapperRows(ctx context.Context, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
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

func (m *MaintenanceManager) readRecordRowMapperRows(ctx context.Context, keys []*pb.RecordKey) ([]*pb.RecordRow, error) {
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
	value = rowkey.NormalizeVersion(strings.TrimSpace(value))
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
