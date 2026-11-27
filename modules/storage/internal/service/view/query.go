package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"

	"trpc.group/trpc-go/trpc-go/log"
)

// QueryTimeSeriesRows pushes the complete predicate, ordering, and pagination
// into the DuckDB engine. The service only owns routing and response shaping.
func (s *Service) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if len(req.GetSelectors()) == 0 && req.GetTimeRange() == nil && req.GetFilter() == nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("query predicate is required"))}, nil
	}
	queryStarted := time.Now()
	indexID, runtime := s.activeIndex(req.GetSpaceId(), req.GetDatasetId())
	resolvedAt := time.Now()
	pin := req.GetPin()
	if expected := strings.TrimSpace(pin.GetIndexId()); expected != "" && expected != indexID {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, fmt.Errorf("active View index changed: expected=%s actual=%s", expected, indexID))}, nil
	}
	startRevision := readIndexRevision(runtime, indexID)
	if startRevision&1 != 0 {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, fmt.Errorf("active View index write in progress: revision=%d", startRevision))}, nil
	}
	if expected := pin.GetRevision(); expected != 0 && expected != startRevision {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, fmt.Errorf("active View index revision changed: expected=%d actual=%d", expected, startRevision))}, nil
	}
	selectors, err := timeSeriesSelectors(req.GetSpaceId(), req.GetDatasetId(), req.GetSelectors())
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	engine, err := engineFor(runtime, indexID)
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
	engineStarted := time.Now()
	rows, total, err := engine.Query(ctx, indexID, spec)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	engineDone := time.Now()
	currentRevision := readIndexRevision(runtime, indexID)
	if elapsed := time.Since(queryStarted); elapsed > time.Second {
		log.Warnf("storage view slow query space=%s dataset=%s index=%s total=%s resolve=%s prepare=%s engine=%s",
			req.GetSpaceId(), req.GetDatasetId(), indexID, elapsed.Round(time.Millisecond), resolvedAt.Sub(queryStarted).Round(time.Millisecond),
			engineStarted.Sub(resolvedAt).Round(time.Millisecond), engineDone.Sub(engineStarted).Round(time.Millisecond))
	}
	if currentRevision != startRevision {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, fmt.Errorf("active View index was updated during query: start=%d current=%d", startRevision, currentRevision))}, nil
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	hidden := runtimeHiddenColumns(runtime)
	for _, row := range rows {
		out = append(out, &pb.TimeSeriesRow{Key: rowToTimeSeriesKey(row.GetKey()), Fields: visibleFields(row.GetFields(), hidden)})
	}
	stats, hasStats := cachedActiveIndexStats(runtime, indexID)
	if req.GetTotalMode() != pb.TotalMode_NONE {
		hasStats = false
		var statsErr error
		stats, statsErr = engine.Stat(ctx, indexID)
		if statsErr != nil {
			log.Warnf("storage view query stat failed space=%s dataset=%s index=%s: %v", req.GetSpaceId(), req.GetDatasetId(), indexID, statsErr)
		} else {
			hasStats = true
			tryCacheActiveIndexStats(runtime, indexID, stats)
			markCoverageRefresh(runtime)
		}
	}
	// Completeness is a coverage property, not a row-count property. A
	// complete query may legitimately return an empty page (for example the
	// trailing page after an exact page-size multiple); treating that page as
	// incomplete makes clients reject an otherwise ready View.
	complete := hasStats && runtimeCoverageComplete(runtime, req.GetTimeRange(), stats)
	minDataTime, maxDataTime := "", ""
	if hasStats {
		minDataTime, maxDataTime = stats.IndexedFrom, stats.IndexedTo
	}
	return &pb.QueryTimeSeriesRowsRsp{
		RetInfo: retinfo.Success("success"), Rows: out, PageResult: makePageResult(pageNo, pageSize, len(out), total),
		MinDataTime: minDataTime, MaxDataTime: maxDataTime, Complete: complete,
		Index: &pb.IndexVersion{IndexId: indexID, Revision: startRevision}, RetainedRowsPerSeries: s.retainedRowsPerSeries(stats, hasStats),
	}, nil
}

// retainedRowsPerSeries 返回活动索引每个序列至少保留的最近行数：索引建立时记录的行数与当前配置中较小的一个。
// 调大配置要等重新建索引后才变大，调小配置立即生效；索引没有记录（记录型 View）时为 0（未知）。
func (s *Service) retainedRowsPerSeries(stats viewindex.ViewIndexStats, hasStats bool) uint64 {
	if !hasStats || stats.RowsPerSeries == 0 {
		return 0
	}
	s.mu.RLock()
	configured := s.seriesBars
	s.mu.RUnlock()
	if configured == 0 || stats.RowsPerSeries < configured {
		return stats.RowsPerSeries
	}
	return configured
}

// readIndexRevision 返回活动索引的内存 Seqlock 修订号（偶数稳定，奇数表示引擎写入进行中），
// 避免查询热路径重复执行 DuckDB 元数据 SQL 与文件 stat，并在读写并发时零漏检地捕获撕裂读。
func readIndexRevision(runtime *viewRuntime, indexID string) uint64 {
	if runtime == nil || indexID == "" {
		return 0
	}
	return runtime.revision.Load()
}

// SearchRecordRows pushes full-text, structured filters, sorting, and paging
// into the Bleve engine.
func (s *Service) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
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
	indexID, runtime := s.activeIndex(req.GetSpaceId(), req.GetDatasetId())
	engine, err := engineFor(runtime, indexID)
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
	hidden := runtimeHiddenColumns(runtime)
	for _, row := range rows {
		out = append(out, &pb.RecordRow{Key: rowToRecordKey(row.GetKey()), Fields: visibleFields(row.GetFields(), hidden)})
	}
	// Record views have no time-window coverage; the active index itself is the completeness signal.
	stats, statsErr := engine.Stat(ctx, indexID)
	complete := statsErr == nil && stats.Exists
	return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, PageResult: makePageResult(pageNo, pageSize, len(out), total), Complete: complete}, nil
}

func runtimeHiddenColumns(runtime *viewRuntime) map[string]struct{} {
	if runtime == nil {
		return nil
	}
	return runtime.queryState().hiddenColumns
}

// visibleFields 去掉被禁用的列：数据集列禁用只是逻辑上的，只在返回时过滤。
func visibleFields(fields []*pb.FieldValue, hidden map[string]struct{}) []*pb.FieldValue {
	if len(hidden) == 0 {
		return fields
	}
	visible := make([]*pb.FieldValue, 0, len(fields))
	for _, field := range fields {
		if _, ok := hidden[field.GetFieldId()]; !ok {
			visible = append(visible, field)
		}
	}
	return visible
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
func queryErrorCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	// 只按哨兵错误判断，不看错误文字：DuckDB 的列绑定等错误是内部错误，不能当成“还没就绪”让调用方无限重试。
	switch {
	case errors.Is(err, errViewIndexNotReady), errors.Is(err, os.ErrNotExist):
		return pb.ErrorCode_VIEW_NOT_READY
	case errors.Is(err, viewindex.ErrColumnNotProjected):
		return pb.ErrorCode_VIEW_COLUMN_NOT_FOUND
	default:
		return pb.ErrorCode_INNER_ERR
	}
}

func queryCoverageComplete(rng *pb.TimeRange, stats viewindex.ViewIndexStats) bool {
	// Persisted bounds are maintained transactionally with writes. They are a
	// stronger signal than the optional full-scan row count, which may be zero
	// on a cold start until the index's first low-frequency scan.
	if !stats.Exists || stats.IndexedFrom == "" || stats.IndexedTo == "" {
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
	return runtime.queryState().active() && queryCoverageComplete(rng, stats)
}

// timeSeriesSelectors 校验并转换时序选择器：每个选择器的空间与数据集都必须等于请求的空间与数据集。
func timeSeriesSelectors(spaceID, datasetID string, selectors []*pb.TimeSeriesSelector) ([]viewindex.TimeSeriesSelector, error) {
	if len(selectors) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(spaceID) == "" {
		return nil, errors.New("space_id is required")
	}
	out := make([]viewindex.TimeSeriesSelector, 0, len(selectors))
	for _, selector := range selectors {
		if selector == nil {
			return nil, errors.New("time-series selector is required")
		}
		if selector.GetSpaceId() == "" || selector.GetDatasetId() == "" ||
			selector.GetSubjectId() == "" || selector.GetFreq() == "" {
			return nil, errors.New("selector space_id, dataset_id, subject_id, and freq are required")
		}
		if selector.GetSpaceId() != spaceID {
			return nil, errors.New("selector space_id does not match the request")
		}
		if selector.GetDatasetId() != datasetID {
			return nil, errors.New("selector dataset_id does not match the request")
		}
		out = append(out, viewindex.TimeSeriesSelector{
			SpaceID: selector.GetSpaceId(), DatasetID: selector.GetDatasetId(),
			SubjectID: selector.GetSubjectId(), Freq: selector.GetFreq(),
			SeriesTag: selector.SeriesTag,
		})
	}
	return out, nil
}
