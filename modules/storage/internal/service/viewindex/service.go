package viewindex

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type ManagedEngine interface {
	coreviewindex.ViewIndexEngine
	List(ctx context.Context) ([]string, error)
}

type TimeSeriesQuerier interface {
	QueryTimeSeriesRows(ctx context.Context, indexID string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error)
}

type RecordQuerier interface {
	QueryRecordRows(ctx context.Context, indexID string, datasetID string, req *pb.SearchRecordRowsReq) ([]*pb.ResultColumn, []*pb.RecordRow, *pb.PageResult, error)
}

type Options struct {
	Engines    map[string]ManagedEngine
	TimeSeries TimeSeriesQuerier
	Records    RecordQuerier
}

type Service struct {
	engines    map[string]ManagedEngine
	timeSeries TimeSeriesQuerier
	records    RecordQuerier
	mu         sync.RWMutex
	schemas    map[string]coreviewindex.ViewIndexSchema
}

var _ pb.ViewIndexService = (*Service)(nil)

func NewService(opts Options) *Service {
	engines := make(map[string]ManagedEngine, len(opts.Engines))
	for name, engine := range opts.Engines {
		if engine != nil {
			engines[strings.ToLower(strings.TrimSpace(name))] = engine
		}
	}
	return &Service{engines: engines, timeSeries: opts.TimeSeries, records: opts.Records, schemas: make(map[string]coreviewindex.ViewIndexSchema)}
}

func (s *Service) PrepareViewIndex(ctx context.Context, req *pb.PrepareViewIndexReq) (*pb.PrepareViewIndexRsp, error) {
	engine, err := s.requestEngine(req.GetEngine())
	if err == nil {
		err = validatePrepareRequest(req)
	}
	if err == nil {
		err = engine.Prepare(ctx, req.GetIndexId(), schemaFromProto(req.GetSchema()))
	}
	if err == nil {
		s.mu.Lock()
		s.schemas[req.GetIndexId()] = schemaFromProto(req.GetSchema())
		s.mu.Unlock()
	}
	if err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: indexError(err)}, nil
	}
	return &pb.PrepareViewIndexRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) ApplyViewIndex(ctx context.Context, req *pb.ApplyViewIndexReq) (*pb.ApplyViewIndexRsp, error) {
	engine, err := s.requestEngine(req.GetEngine())
	batch := batchFromProto(req.GetBatch())
	if err == nil {
		err = validateApplyIndexRequest(req.GetIndexId(), engine, batch)
	}
	if err == nil {
		err = s.validateApplyFence(ctx, req.GetIndexId(), engine, batch)
	}
	if err == nil {
		applier, ok := engine.(coreviewindex.ViewIndexApplier)
		if !ok {
			err = errors.New("view index engine does not support atomic apply")
		} else {
			err = applier.Apply(ctx, req.GetIndexId(), batch)
		}
	}
	if err != nil {
		return &pb.ApplyViewIndexRsp{RetInfo: indexError(err)}, nil
	}
	return &pb.ApplyViewIndexRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) StatViewIndex(ctx context.Context, req *pb.StatViewIndexReq) (*pb.StatViewIndexRsp, error) {
	engine, err := s.requestEngine(req.GetEngine())
	var stats coreviewindex.ViewIndexStats
	if err == nil {
		err = validateIndexID(req.GetIndexId(), engine)
	}
	if err == nil {
		stats, err = engine.Stat(ctx, req.GetIndexId())
	}
	if err != nil {
		return &pb.StatViewIndexRsp{RetInfo: indexError(err)}, nil
	}
	return &pb.StatViewIndexRsp{RetInfo: response.Success("success"), Stats: statsToProto(stats)}, nil
}

func (s *Service) RemoveViewIndex(ctx context.Context, req *pb.RemoveViewIndexReq) (*pb.RemoveViewIndexRsp, error) {
	engine, err := s.requestEngine(req.GetEngine())
	if err == nil {
		err = validateIndexID(req.GetIndexId(), engine)
	}
	if err == nil {
		err = engine.Remove(ctx, req.GetIndexId())
		s.mu.Lock()
		delete(s.schemas, req.GetIndexId())
		s.mu.Unlock()
	}
	if err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: indexError(err)}, nil
	}
	return &pb.RemoveViewIndexRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) ListViewIndexes(ctx context.Context, req *pb.ListViewIndexesReq) (*pb.ListViewIndexesRsp, error) {
	engineNames := make([]string, 0, len(s.engines))
	if name := strings.ToLower(strings.TrimSpace(req.GetEngine())); name != "" {
		engineNames = append(engineNames, name)
	} else {
		for name := range s.engines {
			engineNames = append(engineNames, name)
		}
		sort.Strings(engineNames)
	}
	var descriptors []*pb.ViewIndexDescriptor
	for _, name := range engineNames {
		engine, err := s.requestEngine(name)
		if err != nil {
			return &pb.ListViewIndexesRsp{RetInfo: indexError(err)}, nil
		}
		ids, err := engine.List(ctx)
		if err != nil {
			return &pb.ListViewIndexesRsp{RetInfo: indexError(err)}, nil
		}
		for _, indexID := range ids {
			ref, err := coreviewindex.ParseViewIndexID(indexID)
			if err != nil {
				continue
			}
			if req.GetSpaceId() != "" && req.GetSpaceId() != ref.SpaceID {
				continue
			}
			if req.GetViewId() != "" && req.GetViewId() != ref.ViewID {
				continue
			}
			descriptor := &pb.ViewIndexDescriptor{IndexId: indexID, Engine: name}
			if req.GetIncludeStats() {
				stats, err := engine.Stat(ctx, indexID)
				if err != nil {
					return &pb.ListViewIndexesRsp{RetInfo: indexError(err)}, nil
				}
				descriptor.Stats = statsToProto(stats)
			}
			descriptors = append(descriptors, descriptor)
		}
	}
	return &pb.ListViewIndexesRsp{RetInfo: response.Success("success"), Indexes: descriptors}, nil
}

func (s *Service) QueryTimeSeriesIndex(ctx context.Context, req *pb.QueryTimeSeriesIndexReq) (*pb.QueryTimeSeriesIndexRsp, error) {
	if s.timeSeries == nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: indexError(errors.New("time series index query is unavailable"))}, nil
	}
	if err := validateQueryIndexID(req.GetIndexId(), req.GetQuery().GetSpaceId()); err != nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: indexError(err)}, nil
	}
	columns, rows, page, err := s.timeSeries.QueryTimeSeriesRows(ctx, req.GetIndexId(), req.GetQuery())
	if err != nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: indexError(err)}, nil
	}
	return &pb.QueryTimeSeriesIndexRsp{RetInfo: response.Success("success"), Columns: columns, Rows: rows, PageResult: page}, nil
}

func (s *Service) SearchRecordIndex(ctx context.Context, req *pb.SearchRecordIndexReq) (*pb.SearchRecordIndexRsp, error) {
	if s.records == nil {
		return &pb.SearchRecordIndexRsp{RetInfo: indexError(errors.New("record index query is unavailable"))}, nil
	}
	if err := validateQueryIndexID(req.GetIndexId(), req.GetQuery().GetSpaceId()); err != nil {
		return &pb.SearchRecordIndexRsp{RetInfo: indexError(err)}, nil
	}
	columns, rows, page, err := s.records.QueryRecordRows(ctx, req.GetIndexId(), req.GetDatasetId(), req.GetQuery())
	if err != nil {
		return &pb.SearchRecordIndexRsp{RetInfo: indexError(err)}, nil
	}
	return &pb.SearchRecordIndexRsp{RetInfo: response.Success("success"), Columns: columns, Rows: rows, PageResult: page}, nil
}

func (s *Service) requestEngine(name string) (ManagedEngine, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, errors.New("engine is required")
	}
	engine := s.engines[name]
	if engine == nil {
		return nil, errors.New("unsupported view index engine " + name)
	}
	return engine, nil
}

func validateIndexID(indexID string, engine ManagedEngine) error {
	if strings.TrimSpace(indexID) == "" {
		return errors.New("index_id is required")
	}
	if _, err := coreviewindex.ParseViewIndexID(indexID); err != nil {
		return err
	}
	if engine == nil || strings.TrimSpace(engine.Engine()) == "" {
		return errors.New("engine is required")
	}
	return nil
}

func validateApplyIndexRequest(indexID string, engine ManagedEngine, batch coreviewindex.ViewIndexApplyBatch) error {
	if err := validateIndexID(indexID, engine); err != nil {
		return err
	}
	if batch.ViewVersion == 0 || strings.TrimSpace(batch.ViewSchemaHash) == "" {
		return errors.New("view_version and view_schema_hash are required for fenced index apply")
	}
	ref, _ := coreviewindex.ParseViewIndexID(indexID)
	if err := batch.Validate(); err != nil {
		return err
	}
	for _, row := range batch.RowWrites {
		if row.Key.TimeSeriesKey != nil && row.Key.TimeSeriesKey.GetSpaceId() != "" && row.Key.TimeSeriesKey.GetSpaceId() != ref.SpaceID {
			return errors.New("time series row space_id does not match index_id")
		}
		if row.Key.RecordKey != nil && row.Key.RecordKey.GetSpaceId() != "" && row.Key.RecordKey.GetSpaceId() != ref.SpaceID {
			return errors.New("record row space_id does not match index_id")
		}
	}
	return nil
}

func validateQueryIndexID(indexID string, spaceID string) error {
	if strings.TrimSpace(indexID) == "" || strings.TrimSpace(spaceID) == "" {
		return errors.New("index_id and query space_id are required")
	}
	ref, err := coreviewindex.ParseViewIndexID(indexID)
	if err != nil {
		return err
	}
	if ref.SpaceID != spaceID {
		return errors.New("query space_id does not match index_id")
	}
	return nil
}

func (s *Service) validateApplyFence(ctx context.Context, indexID string, engine ManagedEngine, batch coreviewindex.ViewIndexApplyBatch) error {
	s.mu.RLock()
	schema, prepared := s.schemas[indexID]
	s.mu.RUnlock()
	if prepared {
		if schema.ViewVersion != batch.ViewVersion || schema.SchemaHash != batch.ViewSchemaHash {
			return errors.New("stale view index write rejected by schema fence")
		}
		return nil
	}
	stats, err := engine.Stat(ctx, indexID)
	if err != nil {
		return err
	}
	if !stats.Exists || stats.ViewVersion != batch.ViewVersion || stats.SchemaHash != batch.ViewSchemaHash {
		return errors.New("view index write does not match the prepared physical schema")
	}
	return nil
}

func validatePrepareRequest(req *pb.PrepareViewIndexReq) error {
	if req == nil || strings.TrimSpace(req.GetIndexId()) == "" || req.GetSchema() == nil {
		return errors.New("index_id and schema are required")
	}
	if !strings.EqualFold(strings.TrimSpace(req.GetEngine()), strings.TrimSpace(req.GetSchema().GetEngine())) {
		return errors.New("request engine does not match schema engine")
	}
	ref, err := coreviewindex.ParseViewIndexID(req.GetIndexId())
	if err != nil {
		return err
	}
	if ref.SpaceID != req.GetSchema().GetSpaceId() || ref.ViewID != req.GetSchema().GetViewId() {
		return errors.New("index_id does not match schema space_id/view_id")
	}
	return nil
}

func schemaFromProto(schema *pb.ViewIndexSchema) coreviewindex.ViewIndexSchema {
	if schema == nil {
		return coreviewindex.ViewIndexSchema{}
	}
	return coreviewindex.ViewIndexSchema{
		SpaceID: schema.GetSpaceId(), ViewID: schema.GetViewId(), ViewVersion: schema.GetViewVersion(),
		Engine: schema.GetEngine(), Columns: schema.GetColumns(), SchemaHash: schema.GetViewSchemaHash(),
	}
}

func batchFromProto(batch *pb.ViewIndexApplyBatch) coreviewindex.ViewIndexApplyBatch {
	if batch == nil {
		return coreviewindex.ViewIndexApplyBatch{}
	}
	out := coreviewindex.ViewIndexApplyBatch{ViewVersion: batch.GetViewVersion(), ViewSchemaHash: batch.GetViewSchemaHash(), RequiredColumnNames: append([]string(nil), batch.GetRequiredColumnNames()...)}
	for _, item := range batch.GetRowWrites() {
		if item == nil || item.GetKey() == nil {
			continue
		}
		out.RowWrites = append(out.RowWrites, coreviewindex.RowWrite{
			Operation: coreviewindex.RowWriteOperation(item.GetOperation()),
			Key:       coreviewindex.RowKey{TimeSeriesKey: item.GetKey().GetTimeSeriesKey(), RecordKey: item.GetKey().GetRecordKey()},
			Columns:   item.GetColumns(), Attributes: item.GetAttributes(), AttributesToDelete: item.GetAttributesToDelete(), RemovedColumnNames: item.GetRemovedColumnNames(),
		})
	}
	for _, item := range batch.GetCheckpointUpdates() {
		if item != nil {
			out.CheckpointUpdates = append(out.CheckpointUpdates, coreviewindex.ShardCheckpointUpdate{ShardID: item.GetShardId(), ExpectedLastAppliedSequence: item.GetExpectedLastAppliedSequence(), LastAppliedSequence: item.GetLastAppliedSequence()})
		}
	}
	if update := batch.GetIndexRangeUpdate(); update != nil {
		out.IndexRangeUpdate = &coreviewindex.IndexRangeUpdate{}
		out.IndexRangeUpdate.IndexedFrom = update.IndexedFrom
		out.IndexRangeUpdate.IndexedTo = update.IndexedTo
	}
	return out
}

func statsToProto(stats coreviewindex.ViewIndexStats) *pb.ViewIndexStats {
	count := uint64(0)
	if stats.EntryCount > 0 {
		count = uint64(stats.EntryCount)
	}
	out := &pb.ViewIndexStats{
		Exists: stats.Exists, ViewVersion: stats.ViewVersion, EntryCount: count, MinVersion: stats.MinVersion, MaxVersion: stats.MaxVersion,
		ViewSchemaHash: stats.SchemaHash, PhysicalBytes: stats.PhysicalBytes, UpdatedAt: stats.UpdatedAt, FreeDiskBytes: stats.FreeDiskBytes,
	}
	out.IndexedFrom, out.IndexedTo = stats.IndexedFrom, stats.IndexedTo
	for shardID, sequence := range stats.ShardCheckpoints {
		out.ShardCheckpoints = append(out.ShardCheckpoints, &pb.ViewIndexShardCheckpointUpdate{ShardId: shardID, LastAppliedSequence: sequence})
	}
	return out
}

func statsFromProto(stats *pb.ViewIndexStats) coreviewindex.ViewIndexStats {
	if stats == nil {
		return coreviewindex.ViewIndexStats{}
	}
	count := stats.GetEntryCount()
	if count > math.MaxInt64 {
		count = math.MaxInt64
	}
	out := coreviewindex.ViewIndexStats{
		Exists: stats.GetExists(), ViewVersion: stats.GetViewVersion(), EntryCount: int64(count), MinVersion: stats.GetMinVersion(), MaxVersion: stats.GetMaxVersion(),
		SchemaHash: stats.GetViewSchemaHash(), PhysicalBytes: stats.GetPhysicalBytes(), UpdatedAt: stats.GetUpdatedAt(), FreeDiskBytes: stats.GetFreeDiskBytes(), IndexedFrom: stats.GetIndexedFrom(), IndexedTo: stats.GetIndexedTo(), ShardCheckpoints: make(map[string]uint64),
	}
	for _, checkpoint := range stats.GetShardCheckpoints() {
		if checkpoint != nil {
			out.ShardCheckpoints[checkpoint.GetShardId()] = checkpoint.GetLastAppliedSequence()
		}
	}
	return out
}

func indexError(err error) *pb.RetInfo {
	if err == nil {
		return response.Success("success")
	}
	return response.Error(pb.ErrorCode_INNER_ERR, err)
}
