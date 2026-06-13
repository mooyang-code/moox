package v2

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
)

func (s *Service) QueryFrame(ctx context.Context, req *pb.QueryFrameReq) (*pb.QueryFrameRsp, error) {
	if req.GetWorkspaceId() == "" || len(req.GetInstrumentIds()) == 0 {
		return &pb.QueryFrameRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errText("workspace_id and instrument_ids are required"))}, nil
	}
	columns := req.GetColumns()
	if len(columns) == 0 {
		return &pb.QueryFrameRsp{RetInfo: quantstore.Error(pb.ErrorCode_DATA_VIEW_COLUMN_NOT_FOUND, errText("columns are required"))}, nil
	}

	rowByKey := make(map[string]*pb.QueryFrameRow)
	for _, instrumentID := range req.GetInstrumentIds() {
		for _, column := range columns {
			switch column.GetColumnOrigin() {
			case pb.ColumnOrigin_COLUMN_ORIGIN_UNSPECIFIED, pb.ColumnOrigin_COLUMN_ORIGIN_FIELD:
				datasetID := column.GetDatasetId()
				if datasetID == "" {
					datasetID = inferPrimaryDataset(columns)
				}
				ref := &pb.DataRef{WorkspaceId: req.GetWorkspaceId(), DatasetId: datasetID, InstrumentId: instrumentID}
				points, _, err := s.store.ScanTimeSeries(ctx, ref, req.GetQueryTime().GetTimeRange(), []string{columnFieldName(column)}, req.GetPage())
				if err != nil {
					return &pb.QueryFrameRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
				}
				for _, point := range points {
					row := ensureFrameRow(rowByKey, instrumentID, point.GetTime())
					row.Values = append(row.Values, fieldValueForColumn(column, point.GetFields()))
				}
			case pb.ColumnOrigin_COLUMN_ORIGIN_FACTOR_INSTANCE:
				datasetID := column.GetDatasetId()
				if datasetID == "" {
					datasetID = inferPrimaryDataset(columns)
				}
				ref := &pb.DataRef{WorkspaceId: req.GetWorkspaceId(), DatasetId: datasetID, InstrumentId: instrumentID}
				points, _, err := s.store.ScanFactorValues(ctx, ref, []string{column.GetFactorInstanceId()}, req.GetQueryTime().GetTimeRange(), req.GetPage())
				if err != nil {
					return &pb.QueryFrameRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
				}
				for _, point := range points {
					row := ensureFrameRow(rowByKey, instrumentID, point.GetTime())
					row.Values = append(row.Values, &pb.FieldValue{
						FieldId:   column.GetColumnId(),
						FieldName: outputName(column),
						ValueType: column.GetValueType(),
						Value:     point.GetValue(),
					})
				}
			default:
				return &pb.QueryFrameRsp{RetInfo: quantstore.Error(pb.ErrorCode_QUERY_SHAPE_UNSUPPORTED, errText("expression and system columns are not executable yet"))}, nil
			}
		}
	}

	rows := make([]*pb.QueryFrameRow, 0, len(rowByKey))
	for _, row := range rowByKey {
		rows = append(rows, row)
	}
	sortFrameRows(rows)
	paged, page := pageSlice(rows, req.GetPage())
	return &pb.QueryFrameRsp{RetInfo: quantstore.Success("success"), Columns: columns, Rows: paged, PageResult: page}, nil
}

func (s *Service) TextSearch(ctx context.Context, req *pb.TextSearchReq) (*pb.TextSearchRsp, error) {
	var records []*pb.Record
	for _, instrumentID := range req.GetInstrumentIds() {
		ref := &pb.DataRef{WorkspaceId: req.GetWorkspaceId(), DatasetId: req.GetDatasetId(), InstrumentId: instrumentID}
		rows, _, err := s.store.QueryRecords(ctx, ref, req.GetPage())
		if err != nil {
			return &pb.TextSearchRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		for _, record := range rows {
			if textRecordMatch(record, req.GetQuery()) {
				records = append(records, record)
			}
		}
	}
	paged, page := pageSlice(records, req.GetPage())
	return &pb.TextSearchRsp{RetInfo: quantstore.Success("success"), Records: paged, PageResult: page}, nil
}

func (s *Service) ExplainQuery(_ context.Context, req *pb.ExplainQueryReq) (*pb.ExplainQueryRsp, error) {
	query := req.GetQuery()
	steps := []*pb.QueryPlanStep{
		{StepId: "metadata", Engine: "metadata", Operation: "resolve", Detail: "resolve DataView columns and storage routes"},
		{StepId: "scan", Engine: "pebble-or-file", Operation: "scan_time_series", Detail: "scan requested instruments and time range"},
		{StepId: "join", Engine: "duckdb-or-memory", Operation: "assemble_frame", Detail: "assemble fields and factor instances by instrument/time"},
	}
	if query != nil && query.GetDataViewId() != "" {
		steps[0].Detail += " for " + query.GetDataViewId()
	}
	return &pb.ExplainQueryRsp{RetInfo: quantstore.Success("success"), Steps: steps}, nil
}

func ensureFrameRow(rows map[string]*pb.QueryFrameRow, instrumentID, time string) *pb.QueryFrameRow {
	key := instrumentID + "|" + time
	if rows[key] == nil {
		rows[key] = &pb.QueryFrameRow{InstrumentId: instrumentID, Time: time}
	}
	return rows[key]
}

func fieldValueForColumn(column *pb.QueryFrameColumn, values []*pb.FieldValue) *pb.FieldValue {
	name := columnFieldName(column)
	for _, value := range values {
		if value.GetFieldName() == name || value.GetFieldId() == name {
			return &pb.FieldValue{
				FieldId:   column.GetColumnId(),
				FieldName: outputName(column),
				ValueType: value.GetValueType(),
				Value:     value.GetValue(),
			}
		}
	}
	return &pb.FieldValue{FieldId: column.GetColumnId(), FieldName: outputName(column), ValueType: column.GetValueType()}
}

func columnFieldName(column *pb.QueryFrameColumn) string {
	if column.GetFieldId() != "" {
		return column.GetFieldId()
	}
	return column.GetOutputName()
}

func outputName(column *pb.QueryFrameColumn) string {
	if column.GetOutputName() != "" {
		return column.GetOutputName()
	}
	if column.GetColumnId() != "" {
		return column.GetColumnId()
	}
	return columnFieldName(column)
}

func inferPrimaryDataset(columns []*pb.QueryFrameColumn) string {
	for _, column := range columns {
		if column.GetDatasetId() != "" {
			return column.GetDatasetId()
		}
	}
	return "default"
}

func textRecordMatch(record *pb.Record, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, field := range record.GetFields() {
		if strings.Contains(strings.ToLower(field.GetFieldName()), query) || strings.Contains(strings.ToLower(typedValueString(field.GetValue())), query) {
			return true
		}
	}
	return false
}

func typedValueString(value *pb.TypedValue) string {
	if value == nil {
		return ""
	}
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		return v.StringValue
	case *pb.TypedValue_TimeValue:
		return v.TimeValue
	case *pb.TypedValue_JsonValue:
		return v.JsonValue
	case *pb.TypedValue_IntValue:
		return fmtInt(v.IntValue)
	case *pb.TypedValue_DoubleValue:
		return fmtDouble(v.DoubleValue)
	case *pb.TypedValue_BoolValue:
		if v.BoolValue {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func errText(msg string) error {
	return stringError(msg)
}

type stringError string

func (e stringError) Error() string { return string(e) }
