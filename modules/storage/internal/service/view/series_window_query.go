package view

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

var seriesWindowColumnName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

func (s *Service) querySeriesWindow(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	reject := func(code pb.ErrorCode, err error) (*pb.QueryTimeSeriesRowsRsp, error) {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(code, err)}, nil
	}
	readyError := func(runtime *viewRuntime, err error) (*pb.QueryTimeSeriesRowsRsp, error) {
		rsp := &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_READY, err)}
		if runtime == nil || runtime.active == "" {
			return rsp, nil
		}
		rsp.ServedActiveIndexId = runtime.active
		s.mu.RLock()
		schema, present := s.schemas[runtime.active]
		s.mu.RUnlock()
		if present && schema.SchemaHash != "" {
			rsp.ServedInputContractVersion = schema.SchemaHash + ":" + strconv.FormatUint(schema.ViewVersion, 10)
		}
		return rsp, nil
	}
	count := len(req.GetSelectors())
	if count == 0 || count > 512 || req.GetRowsPerSeries() > 10000 || uint64(count)*uint64(req.GetRowsPerSeries()) > 50000 || req.GetPage() != nil || req.GetLimit() != 0 || len(req.GetSorts()) != 0 || req.GetTotalMode() != pb.TotalMode_NONE || req.GetExpectedActiveIndexRevision() != 0 {
		return reject(pb.ErrorCode_INVALID_PARAM, errors.New("series window requires bounded selectors and rows, without pagination, sorting, exact total or global revision"))
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" || req.GetExpectedInputContractVersion() == "" {
		return reject(pb.ErrorCode_INVALID_PARAM, errors.New("series window requires space and input contract"))
	}
	end, err := time.Parse(time.RFC3339Nano, req.GetTimeRange().GetEndTime())
	if err != nil {
		return reject(pb.ErrorCode_INVALID_PARAM, errors.New("series window requires a valid exclusive end time"))
	}
	if raw := req.GetTimeRange().GetStartTime(); raw != "" {
		start, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || !start.Before(end) {
			return reject(pb.ErrorCode_INVALID_PARAM, errors.New("series window start must precede end"))
		}
	}
	key := viewRef{spaceID: req.GetSpaceId(), viewID: req.GetViewId()}
	s.mu.RLock()
	runtime := s.views[key]
	s.mu.RUnlock()
	if runtime == nil {
		return reject(pb.ErrorCode_VIEW_NOT_READY, errors.New("source View is not active"))
	}
	// Match writer/retirement lock order; keep the physical database alive for
	// the whole single-statement snapshot, without sampling global revisions.
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	index := runtime.active
	if index == "" {
		return readyError(runtime, errors.New("source View is not active"))
	}
	release, err := s.indexWriteGate(index).lock(ctx)
	if err != nil {
		return reject(queryErrorCode(err), err)
	}
	defer release()
	// Prepare releases the physical gate before publishing its new schema.
	// Its marker spans both phases, so never label that file with old metadata.
	if _, preparing := s.preparingGeneration(index); preparing {
		return readyError(runtime, errors.New("source View generation is being prepared"))
	}
	if _, retiring := s.retiringGeneration(index); retiring {
		return readyError(runtime, errors.New("source View generation is retiring"))
	}
	s.mu.RLock()
	schema, present := s.schemas[index]
	engineName := s.indexEngine[index]
	current := s.views[key]
	s.mu.RUnlock()
	contract := schema.SchemaHash + ":" + strconv.FormatUint(schema.ViewVersion, 10)
	if !present || current != runtime || schema.SchemaHash == "" || schema.SpaceID != req.GetSpaceId() || schema.ViewID != req.GetViewId() || schema.PrimaryDatasetID == "" || contract != req.GetExpectedInputContractVersion() {
		return readyError(runtime, errors.New("source View input contract changed"))
	}
	if engineName != "duckdb" {
		return reject(pb.ErrorCode_INVALID_PARAM, errors.New("series windows require a DuckDB source View"))
	}
	var resultColumns []*pb.ResultColumn
	columnSet := make(map[string]struct{}, len(req.GetColumnNames()))
	for _, name := range req.GetColumnNames() {
		columnSet[name] = struct{}{}
	}
	businessColumns := len(columnSet)
	if len(req.GetColumnNames()) == 0 {
		resultColumns, err = fullSeriesWindowColumns(schema.Columns)
		if err != nil {
			return reject(pb.ErrorCode_INVALID_PARAM, err)
		}
		businessColumns = len(resultColumns) - pb.SeriesWindowKeyColumns
	}
	if count > pb.SeriesWindowSubjectLimit(int(req.GetRowsPerSeries()), businessColumns) {
		return reject(pb.ErrorCode_INVALID_PARAM, errors.New("series window exceeds cell budget; reduce selectors or rows_per_series"))
	}
	selectors := make([]viewindex.TimeSeriesSelector, 0, count)
	for _, selector := range req.GetSelectors() {
		if selector.GetSubjectId() == "" || selector.GetFreq() == "" || selector.SeriesTag == nil {
			return reject(pb.ErrorCode_INVALID_PARAM, errors.New("each selector requires subject, frequency and explicit series tag"))
		}
		if (selector.GetSpaceId() != "" && selector.GetSpaceId() != schema.SpaceID) || (selector.GetDatasetId() != "" && selector.GetDatasetId() != schema.PrimaryDatasetID) {
			return reject(pb.ErrorCode_INVALID_PARAM, errors.New("selector scope does not match source View"))
		}
		selectors = append(selectors, viewindex.TimeSeriesSelector{SpaceID: schema.SpaceID, DatasetID: schema.PrimaryDatasetID, SubjectID: selector.GetSubjectId(), Freq: selector.GetFreq(), SeriesTag: selector.SeriesTag})
	}
	engine, err := s.engineFor(index)
	if err != nil {
		return reject(queryErrorCode(err), err)
	}
	rows, _, err := engine.Query(ctx, index, viewindex.QuerySpec{RowsPerSeries: int(req.GetRowsPerSeries()), Selectors: selectors, TimeRange: req.GetTimeRange(), Groups: filterGroups(req.GetFilter()), GroupLogical: filterLogical(req.GetFilter()), Includes: req.GetColumnNames(), TotalMode: pb.TotalMode_NONE})
	if err != nil {
		return reject(queryErrorCode(err), fmt.Errorf("read series window: %w", err))
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.TimeSeriesRow{Key: rowToTimeSeriesKey(row.GetKey()), Fields: row.GetFields(), Attributes: rowAttributesToStrings(row.GetAttributes())})
	}
	// Complete remains unset: bounded query success is not universe coverage.
	rsp := &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, ServedActiveIndexId: index, ServedInputContractVersion: contract}
	if len(req.GetColumnNames()) == 0 {
		// Full-window cache reads need schema even when every row is absent or
		// a business column is entirely NULL. This is the locked physical schema,
		// not a separate metadata read that can race an index replacement.
		rsp.Columns = resultColumns
	}
	return rsp, nil
}

func fullSeriesWindowColumns(columns []*pb.ViewColumn) ([]*pb.ResultColumn, error) {
	result := []*pb.ResultColumn{
		{ColumnName: "subject_id", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM},
		{ColumnName: "freq", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM},
		{ColumnName: "data_time", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_TIME, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM},
		{ColumnName: "series_tag", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM},
	}
	seen := make(map[string]bool, len(columns))
	for _, column := range columns {
		if column == nil {
			continue
		}
		switch column.GetColumnName() {
		case "subject_id", "freq", "data_time", "series_tag", "__moox_attributes":
			continue
		}
		// Legacy physical preparation can omit invalid identifiers or coerce
		// unknown types. Never advertise such a logical column as cache schema.
		name := column.GetColumnName()
		if !seriesWindowColumnName.MatchString(name) || seen[name] || column.GetValueType() < pb.FieldValueType_FIELD_VALUE_TYPE_STRING || column.GetValueType() > pb.FieldValueType_FIELD_VALUE_TYPE_BYTES {
			return nil, fmt.Errorf("source View column %q cannot describe a full physical cache schema", name)
		}
		seen[name] = true
		result = append(result, &pb.ResultColumn{ColumnName: column.GetColumnName(), ValueType: column.GetValueType(), OriginType: column.GetOriginType(), OriginId: column.GetOriginId()})
	}
	sort.Slice(result[4:], func(i, j int) bool { return result[i+4].ColumnName < result[j+4].ColumnName })
	return result, nil
}
