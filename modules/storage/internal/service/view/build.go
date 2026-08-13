package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// errActiveContractUnavailable is a startup-fatal metadata condition. A
// legacy view already mid-rebuild must not fall back to its desired contract:
// doing so would acknowledge rows/markers against the wrong View revision.
var errActiveContractUnavailable = errors.New("active view contract unavailable")

func (s *Service) BackfillView(ctx context.Context, spaceID, viewID string, batchSize int) error {
	return s.BackfillViewWithReader(ctx, spaceID, viewID, batchSize, nil)
}

func (s *Service) BackfillViewWithReader(ctx context.Context, spaceID, viewID string, batchSize int, reader FieldReader) error {
	if batchSize <= 0 {
		batchSize = 100
	}
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	activeID, nextID := runtime.active, runtime.next
	runtime.mu.Unlock()
	if nextID == "" {
		return errors.New("view has no pending build")
	}
	if activeID != "" {
		active, err := s.engineFor(activeID)
		if err != nil {
			return err
		}
		next, err := s.engineFor(nextID)
		if err != nil {
			return err
		}
		s.mu.RLock()
		nextSchema := s.schemas[nextID]
		activeSchema := s.schemas[activeID]
		catalogView := s.catalogViews[viewRef{spaceID: spaceID, viewID: viewID}]
		s.mu.RUnlock()
		for _, timeRange := range backfillTimeRanges(catalogView) {
			var after *pb.RowKey
			for {
				rows, _, err := active.Query(ctx, activeID, viewindex.QuerySpec{
					AfterKey: after, TimeRange: timeRange, Sorts: backfillSorts(active.Engine()), Limit: batchSize, TotalMode: pb.TotalMode_NONE,
				})
				if err != nil {
					return fmt.Errorf("query active view %q for backfill: %w", activeID, err)
				}
				if len(rows) == 0 {
					break
				}
				writes := make([]viewindex.RowWrite, 0, len(rows))
				keys := make([]*pb.RowKey, 0, len(rows))
				for _, row := range rows {
					if row == nil || row.GetKey() == nil {
						continue
					}
					keys = append(keys, row.GetKey())
					writes = append(writes, viewindex.RowWrite{
						Key:        viewindex.RowKey{Key: proto.Clone(row.GetKey()).(*pb.RowKey)},
						Fields:     projectBackfillFields(row.GetFields(), activeSchema, nextSchema),
						Attributes: row.GetAttributes(),
					})
				}
				if len(keys) != 0 {
					// Backfill writes use DuckDB's field-level COALESCE semantics. Do
					// not skip an existing RowKey wholesale: a live delta may contain
					// only one field while the authoritative active row has others.
					// Skipping the row would permanently lose those omitted fields in
					// the newly built index.
				}
				if reader != nil && len(writes) > 0 {
					if err := s.enrichBackfillRows(ctx, reader, activeID, nextID, writes); err != nil {
						return err
					}
				}
				for offset := 0; offset < len(writes); offset += 256 {
					end := offset + 256
					if end > len(writes) {
						end = len(writes)
					}
					if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
						return err
					}
					if err := s.writeIndex(ctx, nextID, next, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: nextSchema.ViewVersion, ViewSchemaHash: nextSchema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
						return fmt.Errorf("write view backfill %q: %w", nextID, err)
					}
				}
				if len(rows) < batchSize {
					break
				}
				after = proto.Clone(rows[len(rows)-1].GetKey()).(*pb.RowKey)
			}
		}
	}
	runtime.mu.Lock()
	if runtime.next != nextID || runtime.buildFailed {
		runtime.mu.Unlock()
		return errViewBuildFailed
	}
	if runtime.next == nextID {
		runtime.status = "ready"
	}
	runtime.mu.Unlock()
	return nil
}

var errViewBuildFailed = errors.New("view build has been marked failed")

func (s *Service) backfillStillActive(spaceID, viewID, nextID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errViewBuildFailed
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.next != nextID || runtime.buildFailed {
		return errViewBuildFailed
	}
	return nil
}

func backfillTimeRanges(view *pb.View) []*pb.TimeRange {
	if view == nil || view.GetKeepDuration() == "" || view.GetKeepDuration() == "0" {
		return []*pb.TimeRange{nil}
	}
	keep, err := time.ParseDuration(view.GetKeepDuration())
	if err != nil || keep <= 0 {
		return []*pb.TimeRange{nil}
	}
	now := time.Now().UTC()
	start := now.Add(-keep)
	const chunk = 5 * time.Minute
	ranges := make([]*pb.TimeRange, 0, int(keep/chunk)+1)
	for start.Before(now) {
		end := start.Add(chunk)
		if end.After(now) {
			end = now
		}
		ranges = append(ranges, &pb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)})
		start = end
	}
	return ranges
}

func backfillSorts(engine string) []*pb.SortSpec {
	if engine == "bleve" {
		return []*pb.SortSpec{{FieldName: "record_id"}, {FieldName: "version"}}
	}
	return []*pb.SortSpec{
		{FieldName: "data_time"}, {FieldName: "subject_id"}, {FieldName: "freq"}, {FieldName: "series_tag"},
		{FieldName: "record_id"}, {FieldName: "version"},
	}
}

func (s *Service) enrichBackfillRows(ctx context.Context, reader FieldReader, activeID, nextID string, writes []viewindex.RowWrite) error {
	s.mu.RLock()
	activeSchema := s.schemas[activeID]
	nextSchema := s.schemas[nextID]
	s.mu.RUnlock()
	activeColumns := make(map[string]viewColumnShape, len(activeSchema.Columns))
	for _, column := range activeSchema.Columns {
		if column != nil {
			activeColumns[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	type requestedField struct {
		source string
		target string
	}
	byDataset := make(map[string][]requestedField)
	for _, column := range nextSchema.Columns {
		if column == nil {
			continue
		}
		if active, exists := activeColumns[column.GetColumnName()]; exists && active.equal(viewColumnShapeOf(column)) {
			continue
		}
		datasetID := viewColumnDataset(column)
		source := viewColumnSource(column, datasetID)
		if datasetID != "" && source != "" {
			byDataset[datasetID] = append(byDataset[datasetID], requestedField{source: source, target: column.GetColumnName()})
		}
	}
	for datasetID, fields := range byDataset {
		keys := make([]*pb.RowKey, 0, len(writes))
		positions := make(map[string]int, len(writes))
		for index, write := range writes {
			key := proto.Clone(write.Key.Key).(*pb.RowKey)
			key.DatasetId = datasetID
			keys = append(keys, key)
			positions[viewindex.RowKeyID(key)] = index
		}
		fieldIDs := make([]string, 0, len(fields))
		targets := make(map[string]string, len(fields))
		for _, field := range fields {
			fieldIDs = append(fieldIDs, field.source)
			targets[field.source] = field.target
		}
		s.mu.RLock()
		auth := s.primaryAuth
		if auth != nil {
			auth = proto.Clone(auth).(*pb.AuthInfo)
		}
		s.mu.RUnlock()
		if auth == nil {
			return errors.New("primary auth is not configured")
		}
		rsp, err := reader.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: auth, Keys: keys, FieldIds: fieldIDs})
		if err != nil {
			return err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return err
		}
		for _, row := range rsp.GetRows() {
			position, ok := positions[viewindex.RowKeyID(row.GetKey())]
			if !ok {
				continue
			}
			for _, field := range row.GetFields() {
				if target := targets[field.GetFieldId()]; target != "" {
					writes[position].Fields = append(writes[position].Fields, &pb.FieldValue{FieldId: target, Value: field.GetValue()})
				}
			}
		}
	}
	return nil
}

type viewColumnShape struct {
	origin     string
	originType pb.ColumnOriginType
	valueType  pb.FieldValueType
}

func viewColumnShapeOf(column *pb.ViewColumn) viewColumnShape {
	if column == nil {
		return viewColumnShape{}
	}
	return viewColumnShape{origin: column.GetOriginId(), originType: column.GetOriginType(), valueType: column.GetValueType()}
}

func (s viewColumnShape) equal(other viewColumnShape) bool {
	return s.origin == other.origin && s.originType == other.originType && s.valueType == other.valueType
}

func projectBackfillFields(fields []*pb.FieldValue, active, next viewindex.ViewIndexSchema) []*pb.FieldValue {
	activeShapes := make(map[string]viewColumnShape, len(active.Columns))
	for _, column := range active.Columns {
		if column != nil {
			activeShapes[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	nextShapes := make(map[string]viewColumnShape, len(next.Columns))
	for _, column := range next.Columns {
		if column != nil {
			nextShapes[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	projected := make([]*pb.FieldValue, 0, len(fields))
	for _, field := range fields {
		if field == nil {
			continue
		}
		name := field.GetFieldId()
		nextShape, ok := nextShapes[name]
		activeShape, activeOK := activeShapes[name]
		if ok && activeOK && activeShape.equal(nextShape) {
			projected = append(projected, field)
		}
	}
	return projected
}

func (s *Service) SwitchView(ctx context.Context, spaceID, viewID string, grace time.Duration) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	oldID, oldGeneration, err := s.switchViewLocked(runtime)
	if err != nil {
		runtime.mu.Unlock()
		return err
	}
	runtime.mu.Unlock()
	s.scheduleOldIndexRemoval(ctx, oldID, oldGeneration, grace)
	// The in-memory and metadata switch is already committed. Do not report a
	// cancellation from the caller's context as a failed build: doing so would
	// discard the newly active index after a successful Activate RPC.
	return nil
}

func (s *Service) switchViewLocked(runtime *viewRuntime) (string, uint64, error) {
	if runtime == nil || runtime.next == "" || runtime.status != "ready" || runtime.buildFailed {
		return "", 0, errors.New("no completed view build to switch")
	}
	oldID := runtime.active
	oldGeneration := s.indexGenerationOf(oldID)
	s.markIndexRetiring(oldID)
	runtime.active = runtime.next
	runtime.activeDatasetIDs = append([]string(nil), runtime.nextDatasetIDs...)
	runtime.activePrimaryDatasetID = runtime.nextPrimaryDatasetID
	runtime.activeDatasetSet = true
	runtime.statsIndexID = ""
	runtime.stats = viewindex.ViewIndexStats{}
	runtime.next = ""
	runtime.nextDatasetIDs = nil
	runtime.nextPrimaryDatasetID = ""
	runtime.status = "active"
	runtime.buildID = ""
	runtime.ownerID = ""
	runtime.metadata = nil
	runtime.metadataAuth = nil
	runtime.buildFailed = false
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	runtime.buildCancel = nil
	runtime.buildContext = nil
	return oldID, oldGeneration, nil
}

func (s *Service) scheduleOldIndexRemoval(ctx context.Context, indexID string, generation uint64, grace time.Duration) {
	if indexID == "" {
		return
	}
	if grace < 0 {
		grace = 0
	}
	s.markIndexRetiring(indexID)
	go func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
			s.removeIndexAfterGrace(context.WithoutCancel(ctx), indexID, generation)
		case <-ctx.Done():
			// A successful switch must still clean the old slot on shutdown.
			s.removeIndexAfterGrace(context.Background(), indexID, generation)
		}
	}()
}

func (s *Service) TrackViewBuild(ctx context.Context, spaceID, viewID, buildID, ownerID string, metadata MetadataClient, auth *pb.AuthInfo) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil || metadata == nil || buildID == "" || ownerID == "" {
		return errors.New("view build tracking requires runtime, metadata, build_id and owner_id")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	buildCtx, cancel := context.WithCancel(ctx)
	runtime.buildContext = buildCtx
	runtime.buildCancel = cancel
	runtime.buildFailed = false
	runtime.buildID = buildID
	runtime.ownerID = ownerID
	runtime.metadata = metadata
	if auth != nil {
		runtime.metadataAuth = proto.Clone(auth).(*pb.AuthInfo)
	}
	return nil
}

func (s *Service) failRuntimeBuild(ctx context.Context, key viewRef, runtime *viewRuntime, cause error) error {
	if runtime == nil || runtime.metadata == nil || runtime.buildID == "" || runtime.ownerID == "" {
		return errors.New("view build failure cannot be persisted")
	}
	if current, err := s.readActiveView(ctx, runtime.metadata, runtime.metadataAuth, key.spaceID, key.viewID); err == nil {
		if build := current.GetIndexBuild(); build != nil && build.GetBuildId() == runtime.buildID && build.GetState() == pb.ViewIndexBuild_FAILED {
			// A previous redelivery may have committed the failure while its RPC
			// response was lost. Treat the state as idempotently persisted so the
			// caller can discard the failed inactive slot.
			runtime.buildFailed = true
			runtime.status = "failing"
			return nil
		}
	}
	runtime.buildFailed = true
	runtime.status = "failing"
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	message := "new view live write failed"
	if cause != nil {
		message = cause.Error()
	}
	rsp, err := runtime.metadata.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		AuthInfo: runtime.metadataAuth, SpaceId: key.spaceID, ViewId: key.viewID,
		BuildId: runtime.buildID, OwnerId: runtime.ownerID, Error: message,
	})
	if err != nil {
		log.Printf("storage view failed to mark build %s/%s as failed: %v", key.spaceID, key.viewID, err)
		return err
	}
	if err := requireSuccess(rsp.GetRetInfo()); err != nil {
		return err
	}
	return nil
}

func (s *Service) AttachActiveView(view *pb.View) error {
	return s.AttachActiveViewWithGrace(context.Background(), view, 0)
}

func (s *Service) AttachActiveViewWithGrace(ctx context.Context, view *pb.View, grace time.Duration) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetActiveIndexId() == "" {
		return errors.New("active view metadata is required")
	}
	// A legacy metadata row may describe an in-flight rebuild without the
	// persisted active contract introduced for A/B views. Falling back to the
	// desired DatasetIds/PrimaryDatasetId would silently route markers and
	// queries to the next revision. Refuse that ambiguous state so startup or
	// reconciliation surfaces an actionable migration error instead.
	if view.GetActiveViewRevision() > 0 && view.GetDesiredViewRevision() > view.GetActiveViewRevision() {
		if len(persistedActiveDatasetIDs(view)) == 0 || strings.TrimSpace(view.GetAttributes()[activePrimaryDatasetAttr]) == "" {
			return fmt.Errorf("%w: active view %s/%s is rebuilding without persisted active contract", errActiveContractUnavailable, view.GetSpaceId(), view.GetViewId())
		}
	}
	engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
	if s.engines[engineName] == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	columns := view.GetActiveColumns()
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		columns = view.GetColumns()
	}
	activePrimaryDatasetID := strings.TrimSpace(view.GetAttributes()[activePrimaryDatasetAttr])
	if activePrimaryDatasetID == "" {
		activePrimaryDatasetID = view.GetPrimaryDatasetId()
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetActiveViewRevision(),
		PrimaryDatasetID: activePrimaryDatasetID, Engine: engineName, Columns: columns, SchemaHash: view.GetActiveViewSchemaHash(),
	}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = &viewRuntime{}
		s.views[viewKey] = runtime
	}
	s.mu.Unlock()
	runtime.mu.Lock()
	previousActive := runtime.active
	activeChanged := previousActive != "" && previousActive != view.GetActiveIndexId()
	previousGeneration := uint64(0)
	if activeChanged {
		// Capture the generation while runtime.mu still excludes PrepareViewIndex
		// for this View. Reading it after unlock could accidentally associate the
		// grace cleanup with a newly reused slot.
		previousGeneration = s.indexGenerationOf(previousActive)
	}
	s.attachActiveViewLocked(view, runtime, schema, columns, activePrimaryDatasetID, engineName)
	runtime.mu.Unlock()
	if activeChanged {
		s.scheduleOldIndexRemoval(ctx, previousActive, previousGeneration, grace)
	}
	return nil
}

// attachActiveViewLocked updates the in-memory active contract. The caller
// must hold runtime.mu; it is used by both restart recovery and the short
// activation critical section.
func (s *Service) attachActiveViewLocked(view *pb.View, runtime *viewRuntime, schema viewindex.ViewIndexSchema, columns []*pb.ViewColumn, activePrimaryDatasetID, engineName string) {
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	activeChanged := runtime.active != "" && runtime.active != view.GetActiveIndexId()
	s.mu.Lock()
	s.catalogViews[viewKey] = proto.Clone(view).(*pb.View)
	s.indexEngine[view.GetActiveIndexId()] = engineName
	s.schemas[view.GetActiveIndexId()] = schema
	if runtime.active != view.GetActiveIndexId() {
		runtime.statsIndexID = ""
		runtime.stats = viewindex.ViewIndexStats{}
	}
	runtime.active = view.GetActiveIndexId()
	// Only initialize the active contract when attaching an index for the first
	// time. During a desired-metadata refresh the same physical active index is
	// re-attached with the new desired DatasetIds; replacing the snapshot here
	// would let period markers observe the next revision before activation.
	if activeChanged || !runtime.activeDatasetSet {
		runtime.activeDatasetIDs = persistedActiveDatasetIDs(view)
		if len(runtime.activeDatasetIDs) == 0 {
			runtime.activeDatasetIDs = append([]string(nil), view.GetDatasetIds()...)
		}
		runtime.activePrimaryDatasetID = activePrimaryDatasetID
		if runtime.activePrimaryDatasetID == "" {
			runtime.activePrimaryDatasetID = view.GetPrimaryDatasetId()
		}
		runtime.activeDatasetSet = true
	}
	if runtime.next == view.GetActiveIndexId() {
		// First activation attaches the index that PrepareViewIndex stored as
		// next. Clear that alias so a live row is not written twice and a
		// transient second write cannot remove the newly active index.
		runtime.next = ""
		runtime.nextDatasetIDs = nil
		runtime.nextPrimaryDatasetID = ""
		runtime.buildID = ""
		runtime.ownerID = ""
		runtime.metadata = nil
		runtime.metadataAuth = nil
		runtime.buildFailed = false
		if runtime.buildCancel != nil {
			runtime.buildCancel()
		}
		runtime.buildCancel = nil
		runtime.buildContext = nil
	}
	runtime.status = "active"
	s.indexView[view.GetActiveIndexId()] = viewKey
	for _, column := range columns {
		if datasetID := viewColumnDataset(column); datasetID != "" {
			ref := datasetRef{spaceID: view.GetSpaceId(), datasetID: datasetID}
			if s.byData[ref] == nil {
				s.byData[ref] = make(map[string]struct{})
			}
			s.byData[ref][view.GetActiveIndexId()] = struct{}{}
		}
	}
	// An explicit zero-column projection still owns its Dataset events. Route
	// those events to the index so rows/markers can be acknowledged without
	// pretending the mapping is missing; the index write is intentionally a
	// key/attribute-only no-op for the empty schema.
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] == "true" {
		s.attachExplicitEmptyDatasetMappingsLocked(view, view.GetActiveIndexId())
	}
	s.mu.Unlock()
}

func (s *Service) attachExplicitEmptyDatasetMappingsLocked(view *pb.View, indexID string) {
	if view == nil || indexID == "" {
		return
	}
	datasetIDs := append([]string(nil), view.GetDatasetIds()...)
	if len(datasetIDs) == 0 && view.GetPrimaryDatasetId() != "" {
		datasetIDs = []string{view.GetPrimaryDatasetId()}
	}
	for _, datasetID := range datasetIDs {
		if datasetID == "" {
			continue
		}
		ref := datasetRef{spaceID: view.GetSpaceId(), datasetID: datasetID}
		if s.byData[ref] == nil {
			s.byData[ref] = make(map[string]struct{})
		}
		s.byData[ref][indexID] = struct{}{}
	}
}

func (s *Service) AttachPendingViewBuild(ctx context.Context, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetIndexBuild() == nil || view.GetIndexBuild().GetIndexId() == "" {
		return errors.New("pending view build metadata is required")
	}
	build := view.GetIndexBuild()
	engineName := strings.ToLower(strings.TrimSpace(build.GetEngine()))
	if engineName == "" {
		engineName = strings.ToLower(strings.TrimSpace(view.GetEngine()))
	}
	engine := s.engines[engineName]
	if engine == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	stats, err := engine.Stat(ctx, build.GetIndexId())
	if err != nil {
		return err
	}
	if !stats.Exists {
		return fmt.Errorf("pending view index %q is missing", build.GetIndexId())
	}
	if expected := build.GetTargetViewVersion(); expected > 0 && stats.ViewVersion != expected {
		return fmt.Errorf("pending view index %q revision mismatch: metadata=%d physical=%d", build.GetIndexId(), expected, stats.ViewVersion)
	}
	columns := build.GetColumns()
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		columns = view.GetColumns()
	}
	version := build.GetTargetViewVersion()
	if version == 0 {
		version = view.GetDesiredViewRevision()
	}
	hash := build.GetSchemaHash()
	if hash == "" {
		hash = viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, PrimaryDatasetID: view.GetPrimaryDatasetId()})
	}
	physicalSchemaHash := viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, PrimaryDatasetID: view.GetPrimaryDatasetId()})
	if hash != physicalSchemaHash {
		return fmt.Errorf("pending view index %q metadata schema hash is stale: build=%q desired=%q", build.GetIndexId(), hash, physicalSchemaHash)
	}
	if stats.SchemaHash != physicalSchemaHash {
		return fmt.Errorf("pending view index %q schema hash mismatch: expected=%q physical=%q", build.GetIndexId(), physicalSchemaHash, stats.SchemaHash)
	}
	schema := viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, SchemaHash: hash, PrimaryDatasetID: view.GetPrimaryDatasetId()}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	// Recovery may attach a prepared slot without calling PrepareViewIndex in
	// this process. Treat it as a fresh generation so an old grace cleanup
	// cannot remove the recovered file.
	s.nextIndexGeneration(build.GetIndexId())
	s.mu.Lock()
	s.catalogViews[viewKey] = proto.Clone(view).(*pb.View)
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = &viewRuntime{}
		s.views[viewKey] = runtime
	}
	s.indexEngine[build.GetIndexId()] = engineName
	s.schemas[build.GetIndexId()] = schema
	s.indexView[build.GetIndexId()] = viewKey
	for _, column := range columns {
		if datasetID := viewColumnDataset(column); datasetID != "" {
			ref := datasetRef{spaceID: view.GetSpaceId(), datasetID: datasetID}
			if s.byData[ref] == nil {
				s.byData[ref] = make(map[string]struct{})
			}
			s.byData[ref][build.GetIndexId()] = struct{}{}
		}
	}
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] == "true" {
		s.attachExplicitEmptyDatasetMappingsLocked(view, build.GetIndexId())
	}
	s.mu.Unlock()
	runtime.mu.Lock()
	if runtime.active == "" {
		runtime.next = build.GetIndexId()
		runtime.nextDatasetIDs = append([]string(nil), view.GetDatasetIds()...)
		runtime.nextPrimaryDatasetID = view.GetPrimaryDatasetId()
		runtime.status = "ready"
	} else if runtime.active != build.GetIndexId() {
		runtime.next = build.GetIndexId()
		runtime.nextDatasetIDs = append([]string(nil), view.GetDatasetIds()...)
		runtime.nextPrimaryDatasetID = view.GetPrimaryDatasetId()
		runtime.status = "ready"
	}
	runtime.mu.Unlock()
	return nil
}

func (s *Service) MarkViewBuildReady(spaceID, viewID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.next == "" {
		runtime.status = "ready"
		return nil
	}
	runtime.status = "ready"
	return nil
}
