package primarystore

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// These RPC handlers preserve the public PrimaryStore row contract while
// exact reads use the field API and range reads use the registered View.
func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("keys are required"))}, nil
	}
	exact := len(req.GetColumnNames()) > 0 && req.GetTimeRange() == nil
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		if key == nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("time-series key is required"))}, nil
		}
		if key.GetDataTime() == "" {
			exact = false
		}
		keys = append(keys, timeSeriesRowKey(key))
	}
	if !exact {
		return s.readTimeSeriesView(ctx, req)
	}
	rsp, err := s.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: req.GetAuthInfo(), Keys: keys, FieldIds: req.GetColumnNames()})
	if err != nil {
		return nil, err
	}
	rows := make([]*pb.TimeSeriesRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		rows = append(rows, &pb.TimeSeriesRow{Key: timeSeriesKeyFromRowKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows}, nil
}

func (s *Service) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("keys are required"))}, nil
	}
	exact := len(req.GetColumnNames()) > 0 && req.GetVersionRange() == nil
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		if key == nil {
			return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("record key is required"))}, nil
		}
		if key.GetVersion() == "" {
			exact = false
		}
		keys = append(keys, recordRowKey(key))
	}
	if !exact {
		return s.readRecordView(ctx, req)
	}
	rsp, err := s.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: req.GetAuthInfo(), Keys: keys, FieldIds: req.GetColumnNames()})
	if err != nil {
		return nil, err
	}
	rows := make([]*pb.RecordRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		rows = append(rows, &pb.RecordRow{Key: recordKeyFromRowKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.ReadRecordRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows}, nil
}

func (s *Service) readTimeSeriesView(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	spaceID, datasetID, err := timeSeriesDataset(req.GetKeys())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if s.view == nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("range reads require a registered DataView"))}, nil
	}
	view, viewID, err := s.view(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	rsp, err := view.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: req.GetAuthInfo(), SpaceId: spaceID, ViewId: viewID, Keys: req.GetKeys(), TimeRange: req.GetTimeRange(), ColumnNames: req.GetColumnNames(), Sorts: []*pb.SortSpec{{FieldName: "data_time", Desc: req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC}}, Page: req.GetPage()})
	if err != nil {
		return nil, err
	}
	rows := make([]*pb.TimeSeriesRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		if row == nil {
			continue
		}
		rows = append(rows, row)
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows, PageResult: rsp.GetPageResult()}, nil
}

func (s *Service) readRecordView(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	spaceID, datasetID, err := recordDataset(req.GetKeys())
	if err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if s.view == nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("range reads require a registered DataView"))}, nil
	}
	view, viewID, err := s.view(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	rsp, err := view.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{AuthInfo: req.GetAuthInfo(), SpaceId: spaceID, ViewId: viewID, Keys: req.GetKeys(), VersionRange: req.GetVersionRange(), Sorts: []*pb.SortSpec{{FieldName: "version", Desc: req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC}}, ColumnNames: req.GetColumnNames(), Page: req.GetPage()})
	if err != nil {
		return nil, err
	}
	rows := make([]*pb.RecordRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		if row == nil {
			continue
		}
		rows = append(rows, row)
	}
	return &pb.ReadRecordRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows, PageResult: rsp.GetPageResult()}, nil
}

func timeSeriesDataset(keys []*pb.TimeSeriesKey) (string, string, error) {
	spaceID, datasetID := "", ""
	for _, key := range keys {
		if key == nil || key.GetSpaceId() == "" || key.GetDatasetId() == "" {
			return "", "", errors.New("space_id and dataset_id are required")
		}
		if spaceID == "" {
			spaceID, datasetID = key.GetSpaceId(), key.GetDatasetId()
		}
		if key.GetSpaceId() != spaceID || key.GetDatasetId() != datasetID {
			return "", "", errors.New("one range read may reference only one space/dataset")
		}
	}
	return spaceID, datasetID, nil
}

func recordDataset(keys []*pb.RecordKey) (string, string, error) {
	spaceID, datasetID := "", ""
	for _, key := range keys {
		if key == nil || key.GetSpaceId() == "" || key.GetDatasetId() == "" {
			return "", "", errors.New("space_id and dataset_id are required")
		}
		if spaceID == "" {
			spaceID, datasetID = key.GetSpaceId(), key.GetDatasetId()
		}
		if key.GetSpaceId() != spaceID || key.GetDatasetId() != datasetID {
			return "", "", errors.New("one range read may reference only one space/dataset")
		}
	}
	return spaceID, datasetID, nil
}

func timeSeriesKeyFromRowKey(key *pb.RowKey) *pb.TimeSeriesKey {
	if key == nil || key.GetTimeSeries() == nil {
		return nil
	}
	return &pb.TimeSeriesKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), SubjectId: key.GetTimeSeries().GetSubjectId(), Freq: key.GetTimeSeries().GetFreq(), DataTime: key.GetTimeSeries().GetDataTime(), Dimensions: key.GetTimeSeries().GetDimensions()}
}

func timeSeriesRowKey(key *pb.TimeSeriesKey) *pb.RowKey {
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), Dimensions: key.GetDimensions()}}}
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
