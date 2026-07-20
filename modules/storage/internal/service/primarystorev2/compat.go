package primarystorev2

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// The row RPCs remain as bounded adapters while workspace callers migrate to
// WriteFields/ReadFields. They deliberately reject range enumeration because
// DataNode exposes only explicit RowKey reads.
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
	rsp, err := s.WriteFields(ctx, &pb.PrimaryWriteFieldsReq{AuthInfo: req.GetAuthInfo(), Rows: rows})
	if err != nil {
		return nil, err
	}
	return &pb.MergeTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo(), WrittenKeys: legacyWrittenKeys(rsp.GetKeys())}, nil
}

func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 || len(req.GetColumnNames()) == 0 {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("explicit keys and column_names are required"))}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		if key == nil || key.GetDataTime() == "" {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("explicit data_time is required"))}, nil
		}
		keys = append(keys, timeSeriesRowKey(key))
	}
	rsp, err := s.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: req.GetAuthInfo(), Keys: keys, FieldIds: req.GetColumnNames()})
	if err != nil {
		return nil, err
	}
	rows := make([]*pb.TimeSeriesRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		rows = append(rows, &pb.TimeSeriesRow{Key: legacyTimeSeriesKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows}, nil
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
		keys = append(keys, legacyRecordKey(key))
	}
	return &pb.MergeRecordRowsRsp{RetInfo: rsp.GetRetInfo(), Keys: keys}, nil
}

func (s *Service) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 || len(req.GetColumnNames()) == 0 {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("explicit keys and column_names are required"))}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		if key == nil {
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("record key is required"))}, nil
		}
		keys = append(keys, recordRowKey(key))
	}
	rsp, err := s.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: req.GetAuthInfo(), Keys: keys, FieldIds: req.GetColumnNames()})
	if err != nil {
		return nil, err
	}
	rows := make([]*pb.RecordRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		rows = append(rows, &pb.RecordRow{Key: legacyRecordKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.ReadRecordRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows}, nil
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

func legacyTimeSeriesKey(key *pb.RowKey) *pb.TimeSeriesKey {
	if key == nil || key.GetTimeSeries() == nil {
		return nil
	}
	return &pb.TimeSeriesKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), SubjectId: key.GetTimeSeries().GetSubjectId(), Freq: key.GetTimeSeries().GetFreq(), DataTime: key.GetTimeSeries().GetDataTime()}
}

func legacyRecordKey(key *pb.RowKey) *pb.RecordKey {
	if key == nil || key.GetRecord() == nil {
		return nil
	}
	return &pb.RecordKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), RecordId: key.GetRecord().GetRecordId(), Version: key.GetRecord().GetVersion()}
}

func legacyWrittenKeys(keys []*pb.RowKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if value := legacyTimeSeriesKey(key); value != nil {
			out = append(out, value.GetSpaceId()+"/"+value.GetDatasetId()+"/"+value.GetSubjectId()+"/"+value.GetFreq()+"/"+value.GetDataTime())
		}
	}
	return out
}
