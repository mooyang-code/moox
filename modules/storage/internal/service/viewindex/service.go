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

func (s *Service) WriteViewIndex(ctx context.Context, req *pb.WriteViewIndexReq) (*pb.WriteViewIndexRsp, error) {
	engine, err := s.requestEngine(req.GetEngine())
	batch := batchFromProto(req.GetBatch())
	if err == nil {
		err = validateWriteIndexRequest(req.GetIndexId(), engine, batch)
	}
	if err == nil {
		err = s.validateWriteFence(ctx, req.GetIndexId(), engine, batch)
	}
	if err == nil {
		err = engine.Write(ctx, req.GetIndexId(), batch)
	}
	if err != nil {
		return &pb.WriteViewIndexRsp{RetInfo: indexError(err)}, nil
	}
	return &pb.WriteViewIndexRsp{RetInfo: response.Success("success")}, nil
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

func validateWriteIndexRequest(indexID string, engine ManagedEngine, batch coreviewindex.ViewIndexBatch) error {
	if err := validateIndexID(indexID, engine); err != nil {
		return err
	}
	if batch.ViewVersion == 0 || strings.TrimSpace(batch.SchemaHash) == "" {
		return errors.New("view_version and schema_hash are required for fenced index writes")
	}
	ref, _ := coreviewindex.ParseViewIndexID(indexID)
	for _, row := range batch.TimeSeriesRows {
		if row.GetKey().GetSpaceId() != "" && row.GetKey().GetSpaceId() != ref.SpaceID {
			return errors.New("time series row space_id does not match index_id")
		}
	}
	for _, row := range batch.RecordRows {
		if row.GetKey().GetSpaceId() != "" && row.GetKey().GetSpaceId() != ref.SpaceID {
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

func (s *Service) validateWriteFence(ctx context.Context, indexID string, engine ManagedEngine, batch coreviewindex.ViewIndexBatch) error {
	s.mu.RLock()
	schema, prepared := s.schemas[indexID]
	s.mu.RUnlock()
	if prepared {
		if schema.ViewVersion != batch.ViewVersion || schema.SchemaHash != batch.SchemaHash {
			return errors.New("stale view index write rejected by schema fence")
		}
		return nil
	}
	stats, err := engine.Stat(ctx, indexID)
	if err != nil {
		return err
	}
	if !stats.Exists || stats.ViewVersion != batch.ViewVersion || stats.SchemaHash != batch.SchemaHash {
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
		Engine: schema.GetEngine(), Columns: schema.GetColumns(), SchemaHash: schema.GetSchemaHash(),
	}
}

func batchFromProto(batch *pb.ViewIndexBatch) coreviewindex.ViewIndexBatch {
	if batch == nil {
		return coreviewindex.ViewIndexBatch{}
	}
	return coreviewindex.ViewIndexBatch{
		TimeSeriesRows: batch.GetTimeSeriesRows(), RecordRows: batch.GetRecordRows(), Columns: batch.GetColumns(),
		ViewVersion: batch.GetViewVersion(), SchemaHash: batch.GetSchemaHash(),
	}
}

func statsToProto(stats coreviewindex.ViewIndexStats) *pb.ViewIndexStats {
	count := uint64(0)
	if stats.EntryCount > 0 {
		count = uint64(stats.EntryCount)
	}
	return &pb.ViewIndexStats{
		Exists: stats.Exists, ViewVersion: stats.ViewVersion, EntryCount: count, MinVersion: stats.MinVersion, MaxVersion: stats.MaxVersion,
		SchemaHash: stats.SchemaHash, PhysicalBytes: stats.PhysicalBytes, UpdatedAt: stats.UpdatedAt, FreeDiskBytes: stats.FreeDiskBytes,
	}
}

func statsFromProto(stats *pb.ViewIndexStats) coreviewindex.ViewIndexStats {
	if stats == nil {
		return coreviewindex.ViewIndexStats{}
	}
	count := stats.GetEntryCount()
	if count > math.MaxInt64 {
		count = math.MaxInt64
	}
	return coreviewindex.ViewIndexStats{
		Exists: stats.GetExists(), ViewVersion: stats.GetViewVersion(), EntryCount: int64(count), MinVersion: stats.GetMinVersion(), MaxVersion: stats.GetMaxVersion(),
		SchemaHash: stats.GetSchemaHash(), PhysicalBytes: stats.GetPhysicalBytes(), UpdatedAt: stats.GetUpdatedAt(), FreeDiskBytes: stats.GetFreeDiskBytes(),
	}
}

func indexError(err error) *pb.RetInfo {
	if err == nil {
		return response.Success("success")
	}
	return response.Error(pb.ErrorCode_INNER_ERR, err)
}
