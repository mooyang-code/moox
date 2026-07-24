package primarystore

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) MergeTimeSeriesRows(ctx context.Context, req *pb.MergeTimeSeriesRowsReq) (*pb.MergeTimeSeriesRowsRsp, error) {
	if req == nil || len(req.GetRows()) == 0 {
		return &pb.MergeTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("rows are required"))}, nil
	}
	rows := make([]*pb.RowFieldUpsert, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		if row == nil || row.GetKey() == nil {
			return &pb.MergeTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("time-series key is required"))}, nil
		}
		rows = append(rows, &pb.RowFieldUpsert{
			Key:        timeSeriesRowKey(row.GetKey()),
			Fields:     legacyFields(row.GetFields(), row.GetColumns()),
			Attributes: stringAttributes(row.GetAttributes()),
		})
	}
	rsp, err := s.WriteFields(ctx, &pb.PrimaryWriteFieldsReq{AuthInfo: req.GetAuthInfo(), Rows: rows, SourceEventId: req.GetSourceEventId()})
	if err != nil {
		return nil, err
	}
	return &pb.MergeTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo(), WrittenKeys: legacyWrittenKeys(rsp.GetKeys())}, nil
}

func (s *Service) MergeRecordRows(ctx context.Context, req *pb.MergeRecordRowsReq) (*pb.MergeRecordRowsRsp, error) {
	if req == nil || len(req.GetRows()) == 0 {
		return &pb.MergeRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("rows are required"))}, nil
	}
	rows := make([]*pb.RowFieldUpsert, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		if row == nil || row.GetKey() == nil {
			return &pb.MergeRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("record key is required"))}, nil
		}
		rows = append(rows, &pb.RowFieldUpsert{
			Key:        recordRowKey(row.GetKey()),
			Fields:     legacyFields(row.GetFields(), row.GetColumns()),
			Attributes: stringAttributes(row.GetAttributes()),
		})
	}
	rsp, err := s.WriteFields(ctx, &pb.PrimaryWriteFieldsReq{AuthInfo: req.GetAuthInfo(), Rows: rows})
	if err != nil {
		return nil, err
	}
	keys := make([]*pb.RecordKey, 0, len(rsp.GetKeys()))
	for _, key := range rsp.GetKeys() {
		keys = append(keys, recordKeyFromRowKey(key))
	}
	return &pb.MergeRecordRowsRsp{RetInfo: rsp.GetRetInfo(), Keys: keys}, nil
}

func legacyFields(fields []*pb.FieldValue, columns []*pb.ColumnValue) []*pb.FieldValue {
	out := append([]*pb.FieldValue(nil), fields...)
	for _, column := range columns {
		if column != nil && column.GetColumnName() != "" && column.GetValue() != nil {
			out = append(out, &pb.FieldValue{FieldId: column.GetColumnName(), Value: column.GetValue()})
		}
	}
	return out
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

func timeSeriesRowKey(key *pb.TimeSeriesKey) *pb.RowKey {
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime()}}}
}

func recordRowKey(key *pb.RecordKey) *pb.RowKey {
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: key.GetRecordId(), Version: key.GetVersion()}}}
}

func recordKeyFromRowKey(key *pb.RowKey) *pb.RecordKey {
	if key == nil || key.GetRecord() == nil {
		return nil
	}
	return &pb.RecordKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), RecordId: key.GetRecord().GetRecordId(), Version: key.GetRecord().GetVersion()}
}

func legacyWrittenKeys(keys []*pb.RowKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key != nil && key.GetTimeSeries() != nil {
			value := key.GetTimeSeries()
			out = append(out, key.GetSpaceId()+"/"+key.GetDatasetId()+"/"+value.GetSubjectId()+"/"+value.GetFreq()+"/"+value.GetDataTime())
		}
	}
	return out
}
