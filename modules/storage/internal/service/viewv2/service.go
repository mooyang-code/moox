package viewv2

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type Service struct{ engine *viewindex.MemoryEngine }

func New(root string) *Service { return &Service{engine: viewindex.NewMemoryEngine("duckdb", root)} }

var _ pb.ViewIndexService = (*Service)(nil)
var _ pb.DataViewService = (*Service)(nil)

func (s *Service) PrepareViewIndex(ctx context.Context, req *pb.PrepareViewIndexReq) (*pb.PrepareViewIndexRsp, error) {
	if req == nil || req.GetSchema() == nil || req.GetIndexId() == "" {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id and schema are required"))}, nil
	}
	sch := req.GetSchema()
	err := s.engine.Prepare(ctx, req.GetIndexId(), viewindex.ViewIndexSchema{SpaceID: sch.GetSpaceId(), ViewID: sch.GetViewId(), ViewVersion: sch.GetViewVersion(), Engine: sch.GetEngine(), Columns: sch.GetColumns(), SchemaHash: sch.GetViewSchemaHash()})
	if err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) ApplyViewIndex(ctx context.Context, req *pb.ApplyViewIndexReq) (*pb.ApplyViewIndexRsp, error) {
	if req == nil || req.GetBatch() == nil || req.GetIndexId() == "" {
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id and batch are required"))}, nil
	}
	b := req.GetBatch()
	writes := make([]viewindex.RowWrite, 0, len(b.GetRowWrites()))
	for _, w := range b.GetRowWrites() {
		if w == nil || w.GetKey() == nil || w.GetKey().GetRowKey() == nil {
			return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row key is required"))}, nil
		}
		writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: w.GetKey().GetRowKey()}, Fields: w.GetFields(), Attributes: w.GetAttributes()})
	}
	mode := viewindex.LiveWrite
	if b.GetWriteMode() == "BACKFILL" {
		mode = viewindex.Backfill
	}
	err := s.engine.Apply(ctx, req.GetIndexId(), viewindex.ViewIndexApplyBatch{RowWrites: writes, ViewRevision: b.GetViewRevision(), ViewSchemaHash: b.GetViewSchemaHash(), WriteMode: mode})
	if err != nil {
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) StatViewIndex(ctx context.Context, req *pb.StatViewIndexReq) (*pb.StatViewIndexRsp, error) {
	if req == nil || req.GetIndexId() == "" {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id is required"))}, nil
	}
	st, err := s.engine.Stat(ctx, req.GetIndexId())
	if err != nil {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.StatViewIndexRsp{RetInfo: retinfo.Success("success"), Stats: &pb.ViewIndexStats{Exists: st.Exists, EntryCount: uint64(st.EntryCount), ViewSchemaHash: st.SchemaHash, ViewVersion: st.ViewVersion, IndexedFrom: st.IndexedFrom, IndexedTo: st.IndexedTo, UpdatedAt: st.UpdatedAt}}, nil
}

func (s *Service) RemoveViewIndex(ctx context.Context, req *pb.RemoveViewIndexReq) (*pb.RemoveViewIndexRsp, error) {
	if req == nil || req.GetIndexId() == "" {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id is required"))}, nil
	}
	if err := s.engine.Remove(ctx, req.GetIndexId()); err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) ListViewIndexes(ctx context.Context, req *pb.ListViewIndexesReq) (*pb.ListViewIndexesRsp, error) {
	ids, err := s.engine.List(ctx)
	if err != nil {
		return &pb.ListViewIndexesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	sort.Strings(ids)
	out := make([]*pb.ViewIndexDescriptor, 0, len(ids))
	for _, id := range ids {
		st, _ := s.engine.Stat(ctx, id)
		out = append(out, &pb.ViewIndexDescriptor{IndexId: id, Engine: s.engine.Engine(), Stats: &pb.ViewIndexStats{Exists: st.Exists, EntryCount: uint64(st.EntryCount), ViewSchemaHash: st.SchemaHash, ViewVersion: st.ViewVersion}})
	}
	return &pb.ListViewIndexesRsp{RetInfo: retinfo.Success("success"), Indexes: out}, nil
}

func (s *Service) QueryTimeSeriesIndex(ctx context.Context, req *pb.QueryTimeSeriesIndexReq) (*pb.QueryTimeSeriesIndexRsp, error) {
	rows, err := s.query(ctx, req.GetIndexId(), req.GetKeys(), req.GetFieldIds())
	if err != nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Success("success"), Rows: rows}, nil
}
func (s *Service) SearchRecordIndex(ctx context.Context, req *pb.SearchRecordIndexReq) (*pb.SearchRecordIndexRsp, error) {
	rows, err := s.query(ctx, req.GetIndexId(), req.GetKeys(), req.GetFieldIds())
	if err != nil {
		return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Success("success"), Rows: rows}, nil
}

// QueryTimeSeriesRows serves the public DataView read contract. The View
// process intentionally accepts explicit keys only; callers that need a
// range first resolve its keys from metadata/PrimaryStore and then issue
// bounded requests. DataNode never exposes a range scan API.
func (s *Service) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	if req == nil || req.GetViewId() == "" || len(req.GetKeys()) == 0 {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id and keys are required"))}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, k := range req.GetKeys() {
		if k == nil {
			return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("time-series key is required"))}, nil
		}
		keys = append(keys, timeSeriesRowKey(k))
	}
	rows, err := s.engine.Query(ctx, req.GetViewId(), keys, req.GetColumnNames())
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.TimeSeriesRow{Key: rowToTimeSeriesKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, Complete: true}, nil
}

// SearchRecordRows is the explicit-key counterpart for record Views. Full
// text/range search remains an upper-layer concern; this process only serves
// materialized rows that the caller names.
func (s *Service) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	if req == nil || req.GetViewId() == "" || len(req.GetKeys()) == 0 {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id and keys are required"))}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, k := range req.GetKeys() {
		if k == nil {
			return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("record key is required"))}, nil
		}
		keys = append(keys, recordRowKey(k))
	}
	rows, err := s.engine.Query(ctx, req.GetViewId(), keys, req.GetColumnNames())
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.RecordRow{Key: rowToRecordKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, Complete: true}, nil
}

func rowToTimeSeriesKey(k *pb.RowKey) *pb.TimeSeriesKey {
	if k == nil || k.GetTimeSeries() == nil {
		return nil
	}
	return &pb.TimeSeriesKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), SubjectId: k.GetTimeSeries().GetSubjectId(), Freq: k.GetTimeSeries().GetFreq(), DataTime: k.GetTimeSeries().GetDataTime()}
}

func timeSeriesRowKey(k *pb.TimeSeriesKey) *pb.RowKey {
	return &pb.RowKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: k.GetSubjectId(), Freq: k.GetFreq(), DataTime: k.GetDataTime()}}}
}

func recordRowKey(k *pb.RecordKey) *pb.RowKey {
	return &pb.RowKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: k.GetRecordId(), Version: k.GetVersion()}}}
}

func rowToRecordKey(k *pb.RowKey) *pb.RecordKey {
	if k == nil || k.GetRecord() == nil {
		return nil
	}
	return &pb.RecordKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), RecordId: k.GetRecord().GetRecordId(), Version: k.GetRecord().GetVersion()}
}
func (s *Service) query(ctx context.Context, id string, keys []*pb.RowKey, fields []string) ([]*pb.RowFieldValues, error) {
	if id == "" || len(keys) == 0 {
		return nil, errors.New("index_id and keys are required")
	}
	rows, err := s.engine.Query(ctx, id, keys, fields)
	if err != nil {
		return nil, fmt.Errorf("query view index: %w", err)
	}
	return rows, nil
}
