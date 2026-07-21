package viewv2

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type MetadataClient interface {
	ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error)
	GetDataset(context.Context, *pb.GetDatasetReq, ...client.Option) (*pb.GetDatasetRsp, error)
	ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error)
	ClaimViewIndexBuild(context.Context, *pb.ClaimViewIndexBuildReq, ...client.Option) (*pb.ClaimViewIndexBuildRsp, error)
	UpdateViewIndexBuild(context.Context, *pb.UpdateViewIndexBuildReq, ...client.Option) (*pb.UpdateViewIndexBuildRsp, error)
	ActivateViewIndex(context.Context, *pb.ActivateViewIndexReq, ...client.Option) (*pb.ActivateViewIndexRsp, error)
	FailViewIndexBuild(context.Context, *pb.FailViewIndexBuildReq, ...client.Option) (*pb.FailViewIndexBuildRsp, error)
}

type FieldReader interface {
	ReadFields(context.Context, *pb.PrimaryReadFieldsReq, ...client.Option) (*pb.PrimaryReadFieldsRsp, error)
}

type PrimaryStoreScanClient interface {
	ScanTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error)
	ScanRecordRows(context.Context, *pb.ReadRecordRowsReq, ...client.Option) (*pb.ReadRecordRowsRsp, error)
}

type ReconcilerOptions struct {
	Metadata MetadataClient
	Primary  FieldReader
	Scan     PrimaryStoreScanClient
	Interval time.Duration
	OwnerID  string
	Grace    time.Duration
}

func (s *Service) StartReconciler(ctx context.Context, opts ReconcilerOptions) (func(), error) {
	if opts.Metadata == nil {
		return nil, errors.New("metadata client is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if strings.TrimSpace(opts.OwnerID) == "" {
		opts.OwnerID = "storage-view"
	}
	if err := s.reconcileOnce(ctx, opts); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				_ = s.reconcileOnce(loopCtx, opts)
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (s *Service) reconcileOnce(ctx context.Context, opts ReconcilerOptions) error {
	auth := s.internalAuth()
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := opts.Metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return err
		}
		for _, view := range rsp.GetViews() {
			if err := s.reconcileView(ctx, opts, auth, view); err != nil {
				continue
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return nil
		}
	}
}

func (s *Service) reconcileView(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return nil
	}
	var stats viewindex.ViewIndexStats
	if view.GetActiveIndexId() != "" {
		if err := s.AttachActiveView(view); err != nil {
			return err
		}
		engine, err := s.engineFor(view.GetActiveIndexId())
		if err != nil {
			return err
		}
		stats, err = engine.Stat(ctx, view.GetActiveIndexId())
		if err != nil {
			return err
		}
	}
	if build := view.GetIndexBuild(); build != nil {
		switch build.GetState() {
		case pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_FAILED:
			if build.GetState() == pb.ViewIndexBuild_FAILED {
				s.removeFailedBuild(ctx, build.GetIndexId())
				return nil
			}
			if build.GetIndexId() == "" {
				return nil
			}
			err := s.resumeViewBuild(ctx, opts, auth, view, build)
			if err != nil {
				var retry resumeBuildRetry
				if !errors.As(err, &retry) {
					s.failBuild(ctx, opts, auth, view, build.GetBuildId(), build.GetIndexId(), err)
				}
			}
			return err
		}
	}
	if !needsRebuild(view, stats) {
		return nil
	}
	columns := view.GetColumns()
	if len(columns) == 0 {
		var err error
		columns, err = loadDefaultViewColumns(ctx, opts.Metadata, auth, view)
		if err != nil {
			return err
		}
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetDesiredViewRevision(),
		PrimaryDatasetID: view.GetPrimaryDatasetId(), Engine: strings.ToLower(view.GetEngine()), Columns: columns,
	}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	indexID := viewindex.InactiveViewIndexID(view.GetSpaceId(), view.GetViewId(), view.GetActiveIndexId())
	buildID := "build-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	claim, err := opts.Metadata.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID,
		IndexId: indexID, Engine: schema.Engine, TargetViewVersion: schema.ViewVersion,
		OwnerId: opts.OwnerID, SchemaHash: schema.SchemaHash, Columns: columns,
		ExpectedActiveIndexId: view.GetActiveIndexId(),
	})
	if err != nil {
		return err
	}
	if err := requireSuccess(claim.GetRetInfo()); err != nil {
		return err
	}
	if view.GetActiveIndexId() != "" {
		if err := s.TrackViewBuild(view.GetSpaceId(), view.GetViewId(), buildID, opts.OwnerID, opts.Metadata, auth); err != nil {
			return err
		}
	}
	prepared, err := s.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
		AuthInfo: auth, IndexId: indexID, Engine: schema.Engine,
		Schema: &pb.ViewIndexSchema{
			SpaceId: schema.SpaceID, ViewId: schema.ViewID, ViewVersion: schema.ViewVersion,
			Engine: schema.Engine, Columns: schema.Columns, ViewSchemaHash: schema.SchemaHash, PrimaryDatasetId: schema.PrimaryDatasetID,
		},
	})
	if err != nil || prepared.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		prepareErr := err
		if prepareErr == nil {
			prepareErr = requireSuccess(prepared.GetRetInfo())
		}
		s.failBuild(ctx, opts, auth, view, buildID, indexID, fmt.Errorf("prepare view index: %w", prepareErr))
		return prepareErr
	}
	if err := s.updateBuild(ctx, opts, auth, view, buildID, pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, 0); err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		return err
	}
	if view.GetActiveIndexId() != "" && stats.Exists {
		if err := s.BackfillViewWithReader(ctx, view.GetSpaceId(), view.GetViewId(), 100, opts.Primary); err != nil {
			s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
			return err
		}
	} else {
		datasetRsp, err := opts.Metadata.GetDataset(ctx, &pb.GetDatasetReq{AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId()})
		if err != nil {
			s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
			return err
		}
		if err := requireSuccess(datasetRsp.GetRetInfo()); err != nil {
			s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
			return err
		}
		if datasetRsp.GetDataset() == nil {
			err := errors.New("primary dataset metadata is missing")
			s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
			return err
		}
		if err := s.BackfillInitialView(ctx, view, indexID, columns, datasetRsp.GetDataset().GetDataKind(), opts.Primary, opts.Scan); err != nil {
			s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
			return err
		}
	}
	if err := s.updateBuild(ctx, opts, auth, view, buildID, pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_READY, uint64(stats.EntryCount)); err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		return err
	}
	activated, err := opts.Metadata.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID, OwnerId: opts.OwnerID,
	})
	if err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		return err
	}
	if err := requireSuccess(activated.GetRetInfo()); err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, indexID, err)
		return err
	}
	if view.GetActiveIndexId() != "" {
		return s.SwitchView(ctx, view.GetSpaceId(), view.GetViewId(), opts.Grace)
	}
	return nil
}

type resumeBuildRetry struct{ cause error }

func (e resumeBuildRetry) Error() string { return e.cause.Error() }
func (e resumeBuildRetry) Unwrap() error { return e.cause }

func (s *Service) resumeViewBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, previous *pb.ViewIndexBuild) error {
	claim, err := opts.Metadata.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: previous.GetBuildId(),
		IndexId: previous.GetIndexId(), Engine: previous.GetEngine(), TargetViewVersion: previous.GetTargetViewVersion(),
		OwnerId: opts.OwnerID, SchemaHash: previous.GetSchemaHash(), Columns: previous.GetColumns(),
		ExpectedActiveIndexId: view.GetActiveIndexId(),
	})
	if err != nil {
		return resumeBuildRetry{cause: err}
	}
	if err := requireSuccess(claim.GetRetInfo()); err != nil {
		return resumeBuildRetry{cause: err}
	}
	build := claim.GetBuild()
	if build == nil {
		return errors.New("resumed view build metadata is missing")
	}
	if err := s.AttachPendingViewBuild(ctx, view); err != nil {
		return err
	}
	if err := s.TrackViewBuild(view.GetSpaceId(), view.GetViewId(), build.GetBuildId(), opts.OwnerID, opts.Metadata, auth); err != nil {
		return err
	}
	if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, build.GetEntriesWritten()); err != nil {
		return err
	}
	activePhysicalExists := view.GetActiveIndexId() != ""
	if activePhysicalExists {
		activeEngine, err := s.engineFor(view.GetActiveIndexId())
		if err != nil {
			return err
		}
		activeStats, err := activeEngine.Stat(ctx, view.GetActiveIndexId())
		if err != nil {
			return err
		}
		activePhysicalExists = activeStats.Exists
	}
	if !activePhysicalExists {
		datasetRsp, err := opts.Metadata.GetDataset(ctx, &pb.GetDatasetReq{AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId()})
		if err != nil {
			return err
		}
		if err := requireSuccess(datasetRsp.GetRetInfo()); err != nil {
			return err
		}
		if datasetRsp.GetDataset() == nil {
			return errors.New("primary dataset metadata is missing")
		}
		columns := build.GetColumns()
		if len(columns) == 0 {
			columns = view.GetColumns()
		}
		if err := s.BackfillInitialView(ctx, view, build.GetIndexId(), columns, datasetRsp.GetDataset().GetDataKind(), opts.Primary, opts.Scan); err != nil {
			return err
		}
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_READY, build.GetEntriesWritten()); err != nil {
			return err
		}
	} else {
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP, build.GetEntriesWritten()); err != nil {
			return err
		}
		if err := s.BackfillViewWithReader(ctx, view.GetSpaceId(), view.GetViewId(), 100, opts.Primary); err != nil {
			return err
		}
		if err := s.updateBuild(ctx, opts, auth, view, build.GetBuildId(), pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_READY, build.GetEntriesWritten()); err != nil {
			return err
		}
	}
	activated, err := opts.Metadata.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: build.GetBuildId(), OwnerId: opts.OwnerID})
	if err != nil {
		return err
	}
	if err := requireSuccess(activated.GetRetInfo()); err != nil {
		return err
	}
	if view.GetActiveIndexId() != "" {
		return s.SwitchView(ctx, view.GetSpaceId(), view.GetViewId(), opts.Grace)
	}
	return nil
}

func loadDefaultViewColumns(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo, view *pb.View) ([]*pb.ViewColumn, error) {
	var columns []*pb.ViewColumn
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListDatasetColumns(ctx, &pb.ListDatasetColumnsReq{
			AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(),
			Page: &pb.Page{Page: pageNo, Size: 1000},
		})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, column := range rsp.GetColumns() {
			if column == nil || (column.GetStatus() != "" && column.GetStatus() != "active") {
				continue
			}
			columns = append(columns, &pb.ViewColumn{
				SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), ColumnName: column.GetColumnName(),
				OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
				OriginId:   view.GetPrimaryDatasetId() + "." + column.GetColumnName(),
				ValueType:  column.GetValueType(),
			})
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetColumns()) == 0 {
			return columns, nil
		}
	}
}

func (s *Service) BackfillInitialView(ctx context.Context, view *pb.View, indexID string, columns []*pb.ViewColumn, dataKind pb.DataKind, reader FieldReader, scan PrimaryStoreScanClient) error {
	if err := s.acquireBackfill(ctx); err != nil {
		return err
	}
	defer s.releaseLiveGate()
	if view == nil || indexID == "" || scan == nil {
		return errors.New("initial view build requires a primary scan client")
	}
	if view.GetPrimaryDatasetId() == "" {
		return errors.New("initial view build requires primary_dataset_id")
	}
	if dataKind != pb.DataKind_DATA_KIND_TIME_SERIES && dataKind != pb.DataKind_DATA_KIND_RECORD {
		return errors.New("initial view build requires a valid primary dataset kind")
	}
	s.mu.RLock()
	schema := s.schemas[indexID]
	s.mu.RUnlock()
	fieldIDs := sourceFieldIDs(columns, view.GetPrimaryDatasetId())
	if len(fieldIDs) == 0 {
		return errors.New("initial view build requires primary dataset columns")
	}
	pageSize := uint32(100)
	pageToken := ""
	for pageNo := uint32(1); ; pageNo++ {
		var writes []viewindex.RowWrite
		var page *pb.PageResult
		nextPageToken := ""
		if dataKind == pb.DataKind_DATA_KIND_TIME_SERIES {
			rsp, err := scan.ScanTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
				AuthInfo: s.primaryAuthClone(), SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(),
				ColumnNames: fieldIDs, Page: &pb.Page{Page: pageNo, Size: pageSize}, PageToken: pageToken,
			})
			if err != nil {
				return err
			}
			if err := requireSuccess(rsp.GetRetInfo()); err != nil {
				return err
			}
			page = rsp.GetPageResult()
			nextPageToken = rsp.GetNextPageToken()
			for _, row := range rsp.GetRows() {
				if row == nil || row.GetKey() == nil {
					continue
				}
				key := &pb.RowKey{SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: row.GetKey().GetSubjectId(), Freq: row.GetKey().GetFreq(), DataTime: row.GetKey().GetDataTime()}}}
				writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: key}, Fields: row.GetFields(), Attributes: stringAttributes(row.GetAttributes())})
			}
		} else {
			rsp, err := scan.ScanRecordRows(ctx, &pb.ReadRecordRowsReq{
				AuthInfo: s.primaryAuthClone(), SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(),
				ColumnNames: fieldIDs, Page: &pb.Page{Page: pageNo, Size: pageSize}, PageToken: pageToken,
			})
			if err != nil {
				return err
			}
			if err := requireSuccess(rsp.GetRetInfo()); err != nil {
				return err
			}
			page = rsp.GetPageResult()
			nextPageToken = rsp.GetNextPageToken()
			for _, row := range rsp.GetRows() {
				if row == nil || row.GetKey() == nil {
					continue
				}
				key := &pb.RowKey{SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: row.GetKey().GetRecordId(), Version: row.GetKey().GetVersion()}}}
				writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: key}, Fields: row.GetFields(), Attributes: stringAttributes(row.GetAttributes())})
			}
		}
		if len(writes) != 0 {
			if reader != nil {
				if err := s.enrichInitialRows(ctx, reader, schema, writes); err != nil {
					return err
				}
			}
			engine, err := s.engineFor(indexID)
			if err != nil {
				return err
			}
			if err := engine.Apply(ctx, indexID, viewindex.ViewIndexApplyBatch{RowWrites: writes, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
				return err
			}
		}
		if page == nil || !page.GetHasMore() || len(writes) == 0 && page.GetTotal() == 0 {
			return s.MarkViewBuildReady(view.GetSpaceId(), view.GetViewId())
		}
		if nextPageToken == "" {
			return errors.New("primary scan returned has_more without a page token")
		}
		pageToken = nextPageToken
	}
}

func (s *Service) primaryAuthClone() *pb.AuthInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.primaryAuth == nil {
		return nil
	}
	return proto.Clone(s.primaryAuth).(*pb.AuthInfo)
}

func sourceFieldIDs(columns []*pb.ViewColumn, datasetID string) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0, len(columns))
	for _, column := range columns {
		if column == nil || viewColumnDataset(column) != datasetID {
			continue
		}
		origin := column.GetOriginId()
		if index := strings.LastIndexByte(origin, '.'); index >= 0 && index+1 < len(origin) {
			origin = origin[index+1:]
		}
		if origin != "" {
			if _, ok := seen[origin]; !ok {
				seen[origin] = struct{}{}
				ids = append(ids, origin)
			}
		}
	}
	return ids
}

func stringAttributes(values map[string]string) map[string]*pb.TypedValue {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]*pb.TypedValue, len(values))
	for key, value := range values {
		out[key] = &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}
	}
	return out
}

func (s *Service) enrichInitialRows(ctx context.Context, reader FieldReader, schema viewindex.ViewIndexSchema, writes []viewindex.RowWrite) error {
	type requested struct{ source, target string }
	byDataset := make(map[string][]requested)
	for _, column := range schema.Columns {
		if column == nil {
			continue
		}
		datasetID := viewColumnDataset(column)
		if datasetID == "" || datasetID == schema.PrimaryDatasetID {
			continue
		}
		origin := column.GetOriginId()
		if index := strings.LastIndexByte(origin, '.'); index >= 0 && index+1 < len(origin) {
			origin = origin[index+1:]
		}
		if origin != "" {
			byDataset[datasetID] = append(byDataset[datasetID], requested{source: origin, target: column.GetColumnName()})
		}
	}
	for datasetID, fields := range byDataset {
		keys := make([]*pb.RowKey, 0, len(writes))
		positions := make(map[string]int, len(writes))
		fieldIDs := make([]string, 0, len(fields))
		targets := make(map[string]string, len(fields))
		for index, write := range writes {
			key := proto.Clone(write.Key.Key).(*pb.RowKey)
			key.DatasetId = datasetID
			keys = append(keys, key)
			positions[viewindex.RowKeyID(key)] = index
		}
		for _, field := range fields {
			fieldIDs = append(fieldIDs, field.source)
			targets[field.source] = field.target
		}
		auth := s.primaryAuthClone()
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

func (s *Service) updateBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID string, from, to pb.ViewIndexBuild_State, rows uint64) error {
	rsp, err := opts.Metadata.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID,
		OwnerId: opts.OwnerID, ExpectedState: from, NextState: to, EntriesWritten: rows,
	})
	if err != nil {
		return err
	}
	return requireSuccess(rsp.GetRetInfo())
}

func (s *Service) failBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID, indexID string, cause error) {
	message := "view build failed"
	if cause != nil {
		message = cause.Error()
	}
	_, _ = opts.Metadata.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(),
		BuildId: buildID, OwnerId: opts.OwnerID, Error: message,
	})
	s.discardFailedBuild(ctx, view.GetSpaceId(), view.GetViewId(), indexID)
}

func (s *Service) discardFailedBuild(ctx context.Context, spaceID, viewID, indexID string) {
	if indexID == "" {
		return
	}
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	if runtime.next == indexID {
		runtime.next = ""
		runtime.status = "active"
	} else if runtime.active == indexID {
		runtime.active = ""
		runtime.status = "failed"
	}
	runtime.mu.Unlock()
	s.removeFailedBuild(ctx, indexID)
}

func needsRebuild(view *pb.View, stats viewindex.ViewIndexStats) bool {
	if view == nil {
		return false
	}
	if view.GetActiveIndexId() == "" || view.GetDesiredViewRevision() > view.GetActiveViewRevision() {
		return true
	}
	if !stats.Exists {
		return true
	}
	keep, err := time.ParseDuration(view.GetKeepDuration())
	if err != nil || keep <= 0 || stats.IndexedFrom == "" || stats.IndexedTo == "" {
		return false
	}
	from, err := time.Parse(time.RFC3339Nano, stats.IndexedFrom)
	if err != nil {
		return false
	}
	to, err := time.Parse(time.RFC3339Nano, stats.IndexedTo)
	return err == nil && to.Sub(from) > 2*keep
}

func (s *Service) internalAuth() *pb.AuthInfo {
	const appID = "storage-view"
	return &pb.AuthInfo{AppId: appID, AppKey: datanode.ServiceAuthKey(s.authSecret, appID)}
}

func requireSuccess(ret *pb.RetInfo) error {
	if ret == nil {
		return errors.New("empty ret_info")
	}
	if ret.GetCode() != pb.ErrorCode_SUCCESS {
		return errors.New(ret.GetMsg())
	}
	return nil
}
