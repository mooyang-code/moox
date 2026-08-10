package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) QueryTimeSeriesIndex(ctx context.Context, req *pb.QueryTimeSeriesIndexReq) (*pb.QueryTimeSeriesIndexRsp, error) {
	if req == nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows, err := s.query(ctx, req.GetIndexId(), req.GetKeys(), req.GetFieldIds())
	if err != nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Success("success"), Rows: rows}, nil
}
func (s *Service) SearchRecordIndex(ctx context.Context, req *pb.SearchRecordIndexReq) (*pb.SearchRecordIndexRsp, error) {
	if req == nil {
		return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows, err := s.query(ctx, req.GetIndexId(), req.GetKeys(), req.GetFieldIds())
	if err != nil {
		return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Success("success"), Rows: rows}, nil
}

// QueryTimeSeriesRows pushes the complete predicate, ordering, and pagination
// into the DuckDB engine. The service only owns routing and response shaping.
func (s *Service) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	if req == nil || req.GetViewId() == "" {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if len(req.GetSelectors()) == 0 && req.GetTimeRange() == nil && req.GetFilter() == nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("query predicate is required"))}, nil
	}
	indexID, runtime := s.activeIndex(req.GetSpaceId(), req.GetViewId())
	selectors, err := s.timeSeriesSelectors(indexID, req)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	engine, err := s.engineFor(indexID)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	pageNo, pageSize := queryPage(req.GetPage())
	limit := pageSize
	if req.GetLimit() > 0 {
		limit = int(req.GetLimit())
	}
	offset := (pageNo - 1) * pageSize
	if req.GetLimit() > 0 {
		offset = 0
	}
	spec := viewindex.QuerySpec{Selectors: selectors, TimeRange: req.GetTimeRange(), Groups: filterGroups(req.GetFilter()), GroupLogical: filterLogical(req.GetFilter()), Sorts: req.GetSorts(), Includes: req.GetColumnNames(), Offset: offset, Limit: limit, TotalMode: req.GetTotalMode()}
	rows, total, err := engine.Query(ctx, indexID, spec)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.TimeSeriesRow{Key: rowToTimeSeriesKey(row.GetKey()), Fields: row.GetFields()})
	}
	stats, hasStats := cachedActiveIndexStats(runtime, indexID)
	if req.GetTotalMode() != pb.TotalMode_NONE {
		hasStats = false
		var statsErr error
		stats, statsErr = engine.Stat(ctx, indexID)
		if statsErr != nil {
			log.Printf("storage view query stat failed space=%s view=%s index=%s: %v", req.GetSpaceId(), req.GetViewId(), indexID, statsErr)
		} else {
			hasStats = true
			s.cacheActiveIndexStats(runtime, indexID, stats)
		}
	}
	complete := hasStats && len(out) > 0 && runtimeCoverageComplete(runtime, req.GetTimeRange(), stats)
	indexedFrom, indexedTo := "", ""
	if hasStats {
		indexedFrom, indexedTo = stats.IndexedFrom, stats.IndexedTo
	}
	return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: makePageResult(pageNo, pageSize, len(out), total), ServedIndexedFrom: indexedFrom, ServedIndexedTo: indexedTo, Complete: complete}, nil
}

// SearchRecordRows pushes full-text, structured filters, sorting, and paging
// into the Bleve engine.
func (s *Service) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	if req == nil || req.GetViewId() == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if len(req.GetKeys()) == 0 && strings.TrimSpace(req.GetTextQuery()) == "" && req.GetFilter() == nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("query predicate is required"))}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, k := range req.GetKeys() {
		if k == nil {
			return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("record key is required"))}, nil
		}
		keys = append(keys, recordRowKey(k))
	}
	indexID, _ := s.activeIndex(req.GetSpaceId(), req.GetViewId())
	engine, err := s.engineFor(indexID)
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	pageNo, pageSize := queryPage(req.GetPage())
	spec := viewindex.QuerySpec{Keys: keys, VersionRange: req.GetVersionRange(), TextQuery: req.GetTextQuery(), Groups: filterGroups(req.GetFilter()), GroupLogical: filterLogical(req.GetFilter()), Sorts: req.GetSorts(), Includes: req.GetColumnNames(), Offset: (pageNo - 1) * pageSize, Limit: pageSize, TotalMode: pb.TotalMode_AUTO}
	rows, total, err := engine.Query(ctx, indexID, spec)
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.RecordRow{Key: rowToRecordKey(row.GetKey()), Fields: row.GetFields()})
	}
	// Record views have no time-window coverage; the active index itself is the completeness signal.
	stats, statsErr := engine.Stat(ctx, indexID)
	complete := statsErr == nil && stats.Exists
	return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: makePageResult(pageNo, pageSize, len(out), total), Complete: complete}, nil
}

func filterGroups(spec *pb.FilterSpec) []viewindex.FilterGroup {
	if spec == nil {
		return nil
	}
	groups := make([]viewindex.FilterGroup, 0, len(spec.GetGroups()))
	for _, group := range spec.GetGroups() {
		if group == nil {
			continue
		}
		out := viewindex.FilterGroup{Logical: group.GetLogical()}
		for _, cond := range group.GetConds() {
			if cond != nil {
				out.Conds = append(out.Conds, viewindex.Filter{Column: cond.GetColumn(), Op: cond.GetOp(), Values: cond.GetValues()})
			}
		}
		groups = append(groups, out)
	}
	return groups
}

func filterLogical(spec *pb.FilterSpec) pb.FilterLogical {
	if spec == nil {
		return pb.FilterLogical_FILTER_LOGICAL_AND
	}
	return spec.GetGroupLogical()
}

func makePageResult(page, size int, returned int, total int64) *pb.PageResult {
	result := &pb.PageResult{Page: uint32(page), Size: uint32(size), Total: uint32(maxInt64(total, 0))}
	if total >= 0 {
		result.HasMore = int64((page-1)*size+returned) < total
	} else {
		result.HasMore = returned >= size
	}
	return result
}

func maxInt64(value, fallback int64) int64 {
	if value < fallback {
		return fallback
	}
	return value
}

func queryPage(page *pb.Page) (int, int) {
	pageNo, size := 1, 100
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = int(page.GetPage())
		}
		if page.GetSize() > 0 {
			size = int(page.GetSize())
		}
	}
	return pageNo, size
}

func rowToTimeSeriesKey(k *pb.RowKey) *pb.TimeSeriesKey {
	if k == nil || k.GetTimeSeries() == nil {
		return nil
	}
	return &pb.TimeSeriesKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), SubjectId: k.GetTimeSeries().GetSubjectId(), Freq: k.GetTimeSeries().GetFreq(), DataTime: k.GetTimeSeries().GetDataTime(), SeriesTag: k.GetTimeSeries().GetSeriesTag()}
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
	engine, err := s.engineFor(id)
	if err != nil {
		return nil, err
	}
	rows, _, err := engine.Query(ctx, id, viewindex.QuerySpec{Keys: keys, Includes: fields, TotalMode: pb.TotalMode_NONE})
	if err != nil {
		return nil, fmt.Errorf("query view index: %w", err)
	}
	return rows, nil
}

// StartEventConsumer binds the configured managed durable and applies field
// events to prepared indexes. It deliberately uses one Fetch(1) loop; the

func queryErrorCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	if errors.Is(err, errViewIndexNotReady) || errors.Is(err, os.ErrNotExist) {
		return pb.ErrorCode_VIEW_NOT_READY
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "view column") && (strings.Contains(msg, "not projected") || strings.Contains(msg, "ambiguous")) {
		return pb.ErrorCode_VIEW_COLUMN_NOT_FOUND
	}
	if strings.Contains(msg, "is not prepared") || strings.Contains(msg, "no such file") || strings.Contains(msg, "not found") {
		return pb.ErrorCode_VIEW_NOT_READY
	}
	return pb.ErrorCode_INNER_ERR
}

func queryCoverageComplete(rng *pb.TimeRange, stats viewindex.ViewIndexStats) bool {
	if !stats.Exists || stats.EntryCount == 0 || stats.IndexedFrom == "" || stats.IndexedTo == "" {
		return false
	}
	if rng == nil {
		return false
	}
	indexedFrom, err := time.Parse(time.RFC3339Nano, stats.IndexedFrom)
	if err != nil {
		return false
	}
	indexedTo, err := time.Parse(time.RFC3339Nano, stats.IndexedTo)
	if err != nil {
		return false
	}
	start := strings.TrimSpace(rng.GetStartTime())
	end := strings.TrimSpace(rng.GetEndTime())
	if start == "" || end == "" {
		return false
	}
	requestedStart, err := time.Parse(time.RFC3339Nano, start)
	if err != nil || requestedStart.Before(indexedFrom) {
		return false
	}
	requestedEnd, err := time.Parse(time.RFC3339Nano, end)
	if err != nil || !requestedEnd.After(requestedStart) {
		return false
	}
	// IndexedTo is the inclusive maximum data_time while the query end is
	// exclusive. One nanosecond beyond the maximum is therefore still covered.
	if requestedEnd.After(indexedTo) && requestedEnd.Sub(indexedTo) > time.Nanosecond {
		return false
	}
	return true
}

func runtimeCoverageComplete(runtime *viewRuntime, rng *pb.TimeRange, stats viewindex.ViewIndexStats) bool {
	if runtime == nil {
		return false
	}
	runtime.mu.Lock()
	active := runtime.active != ""
	runtime.mu.Unlock()
	return active && queryCoverageComplete(rng, stats)
}

func (s *Service) timeSeriesSelectors(indexID string, req *pb.QueryTimeSeriesRowsReq) ([]viewindex.TimeSeriesSelector, error) {
	if len(req.GetSelectors()) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return nil, errors.New("space_id is required")
	}
	s.mu.RLock()
	schema, ok := s.schemas[indexID]
	s.mu.RUnlock()
	if !ok || schema.SpaceID == "" || schema.PrimaryDatasetID == "" {
		return nil, errors.New("view selector scope is unavailable")
	}
	if schema.SpaceID != req.GetSpaceId() {
		return nil, errors.New("request space_id does not match the view")
	}
	selectors := make([]viewindex.TimeSeriesSelector, 0, len(req.GetSelectors()))
	for _, selector := range req.GetSelectors() {
		if selector == nil {
			return nil, errors.New("time-series selector is required")
		}
		if selector.GetSpaceId() == "" || selector.GetDatasetId() == "" ||
			selector.GetSubjectId() == "" || selector.GetFreq() == "" {
			return nil, errors.New("selector space_id, dataset_id, subject_id, and freq are required")
		}
		if selector.GetSpaceId() != req.GetSpaceId() || selector.GetSpaceId() != schema.SpaceID {
			return nil, errors.New("selector space_id does not match the view")
		}
		if selector.GetDatasetId() != schema.PrimaryDatasetID {
			return nil, errors.New("selector dataset_id does not match the view")
		}
		selectors = append(selectors, viewindex.TimeSeriesSelector{
			SpaceID: selector.GetSpaceId(), DatasetID: selector.GetDatasetId(),
			SubjectID: selector.GetSubjectId(), Freq: selector.GetFreq(),
			SeriesTag: selector.SeriesTag,
		})
	}
	return selectors, nil
}
