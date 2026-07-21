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

func (s *Service) BackfillView(ctx context.Context, spaceID, viewID string, batchSize int) error {
	return s.BackfillViewWithReader(ctx, spaceID, viewID, batchSize, nil)
}

func (s *Service) BackfillViewWithReader(ctx context.Context, spaceID, viewID string, batchSize int, reader FieldReader) error {
	if err := s.acquireBackfill(ctx); err != nil {
		return err
	}
	defer s.releaseLiveGate()
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
		s.mu.RUnlock()
		var after *pb.RowKey
		for {
			rows, _, err := active.Query(ctx, activeID, viewindex.QuerySpec{
				AfterKey: after, Sorts: backfillSorts(active.Engine()), Limit: batchSize, TotalMode: pb.TotalMode_NONE,
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
				writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: proto.Clone(row.GetKey()).(*pb.RowKey)}, Fields: row.GetFields(), Attributes: row.GetAttributes()})
			}
			if len(keys) != 0 {
				liveRows, _, err := next.Query(ctx, nextID, viewindex.QuerySpec{Keys: keys, TotalMode: pb.TotalMode_NONE})
				if err != nil {
					return fmt.Errorf("check live rows in pending view %q: %w", nextID, err)
				}
				live := make(map[string]struct{}, len(liveRows))
				for _, row := range liveRows {
					if row != nil && row.GetKey() != nil {
						live[viewindex.RowKeyID(row.GetKey())] = struct{}{}
					}
				}
				filtered := writes[:0]
				for _, write := range writes {
					if _, exists := live[viewindex.RowKeyID(write.Key.Key)]; !exists {
						filtered = append(filtered, write)
					}
				}
				writes = filtered
			}
			if reader != nil && len(writes) > 0 {
				if err := s.enrichBackfillRows(ctx, reader, activeID, nextID, writes); err != nil {
					return err
				}
			}
			if len(writes) > 0 {
				if err := next.Write(ctx, nextID, viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: nextSchema.ViewVersion, ViewSchemaHash: nextSchema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
					return fmt.Errorf("write view backfill %q: %w", nextID, err)
				}
			}
			if len(rows) < batchSize {
				break
			}
			after = proto.Clone(rows[len(rows)-1].GetKey()).(*pb.RowKey)
		}
	}
	runtime.mu.Lock()
	if runtime.next == nextID {
		runtime.status = "ready"
	}
	runtime.mu.Unlock()
	return nil
}

func backfillSorts(engine string) []*pb.SortSpec {
	if engine == "bleve" {
		return []*pb.SortSpec{{FieldName: "record_id"}, {FieldName: "version"}}
	}
	return []*pb.SortSpec{
		{FieldName: "subject_id"}, {FieldName: "freq"}, {FieldName: "data_time"},
		{FieldName: "record_id"}, {FieldName: "version"},
	}
}

func (s *Service) waitForLiveIdle(ctx context.Context) error {
	for s.liveWork.Load() > 0 {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (s *Service) enrichBackfillRows(ctx context.Context, reader FieldReader, activeID, nextID string, writes []viewindex.RowWrite) error {
	s.mu.RLock()
	activeSchema := s.schemas[activeID]
	nextSchema := s.schemas[nextID]
	s.mu.RUnlock()
	activeColumns := make(map[string]struct{}, len(activeSchema.Columns))
	for _, column := range activeSchema.Columns {
		if column != nil {
			activeColumns[column.GetColumnName()] = struct{}{}
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
		if _, exists := activeColumns[column.GetColumnName()]; exists {
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

func (s *Service) SwitchView(ctx context.Context, spaceID, viewID string, grace time.Duration) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	if runtime.next == "" || runtime.status != "ready" {
		runtime.mu.Unlock()
		return errors.New("no completed view build to switch")
	}
	oldID := runtime.active
	runtime.active = runtime.next
	runtime.next = ""
	runtime.status = "active"
	runtime.buildID = ""
	runtime.ownerID = ""
	runtime.metadata = nil
	runtime.metadataAuth = nil
	runtime.mu.Unlock()
	if grace < 0 {
		grace = 0
	}
	go func() {
		timer := time.NewTimer(grace)
		<-timer.C
		s.removeFailedBuild(context.WithoutCancel(ctx), oldID)
	}()
	return ctx.Err()
}

func (s *Service) TrackViewBuild(spaceID, viewID, buildID, ownerID string, metadata MetadataClient, auth *pb.AuthInfo) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil || metadata == nil || buildID == "" || ownerID == "" {
		return errors.New("view build tracking requires runtime, metadata, build_id and owner_id")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.buildID = buildID
	runtime.ownerID = ownerID
	runtime.metadata = metadata
	if auth != nil {
		runtime.metadataAuth = proto.Clone(auth).(*pb.AuthInfo)
	}
	return nil
}

func (s *Service) failRuntimeBuild(ctx context.Context, key viewRef, runtime *viewRuntime, cause error) {
	if runtime == nil || runtime.metadata == nil || runtime.buildID == "" || runtime.ownerID == "" {
		return
	}
	message := "new view live write failed"
	if cause != nil {
		message = cause.Error()
	}
	if _, err := runtime.metadata.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		AuthInfo: runtime.metadataAuth, SpaceId: key.spaceID, ViewId: key.viewID,
		BuildId: runtime.buildID, OwnerId: runtime.ownerID, Error: message,
	}); err != nil {
		log.Printf("storage view failed to mark build %s/%s as failed: %v", key.spaceID, key.viewID, err)
	}
}

func (s *Service) AttachActiveView(view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetActiveIndexId() == "" {
		return errors.New("active view metadata is required")
	}
	engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
	if s.engines[engineName] == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	columns := view.GetActiveColumns()
	if len(columns) == 0 {
		columns = view.GetColumns()
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetActiveViewRevision(),
		PrimaryDatasetID: view.GetPrimaryDatasetId(), Engine: engineName, Columns: columns, SchemaHash: view.GetActiveViewSchemaHash(),
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
	defer runtime.mu.Unlock()
	s.mu.Lock()
	s.catalogViews[viewKey] = proto.Clone(view).(*pb.View)
	s.indexEngine[view.GetActiveIndexId()] = engineName
	s.schemas[view.GetActiveIndexId()] = schema
	runtime.active = view.GetActiveIndexId()
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
	s.mu.Unlock()
	return nil
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
	columns := build.GetColumns()
	if len(columns) == 0 {
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
	schema := viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, SchemaHash: hash, PrimaryDatasetID: view.GetPrimaryDatasetId()}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
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
	s.mu.Unlock()
	runtime.mu.Lock()
	if runtime.active == "" {
		runtime.next = build.GetIndexId()
		runtime.status = "ready"
	} else if runtime.active != build.GetIndexId() {
		runtime.next = build.GetIndexId()
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
