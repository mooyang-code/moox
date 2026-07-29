package primarystore

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// These RPC handlers preserve the public PrimaryStore row contract while
// range reads use a registered View. Exact point reads use ReadFields.
func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil || len(req.GetSelectors()) == 0 {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("selectors are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	return s.readTimeSeriesView(ctx, req)
}

func (s *Service) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("keys are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
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
	spaceID, datasetID, err := timeSeriesDataset(req)
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
	if view == nil || viewID == "" {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_VIEW_NOT_FOUND, errors.New("range reads require a registered DataView"))}, nil
	}
	desc := req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC
	sorts := []*pb.SortSpec{
		{FieldName: "subject_id", Desc: desc},
		{FieldName: "freq", Desc: desc},
		{FieldName: "data_time", Desc: desc},
		{FieldName: "series_tag", Desc: desc},
	}
	rsp, err := view.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: req.GetAuthInfo(), SpaceId: spaceID, ViewId: viewID, Selectors: req.GetSelectors(), TimeRange: req.GetTimeRange(), ColumnNames: req.GetColumnNames(), Sorts: sorts, Page: req.GetPage()})
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
	return &pb.ReadTimeSeriesRowsRsp{
		RetInfo: rsp.GetRetInfo(), Rows: rows, PageResult: rsp.GetPageResult(),
		ServedIndexedFrom: rsp.GetServedIndexedFrom(),
		ServedIndexedTo:   rsp.GetServedIndexedTo(),
		Complete:          rsp.GetComplete(),
	}, nil
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

func timeSeriesDataset(req *pb.ReadTimeSeriesRowsReq) (string, string, error) {
	spaceID, datasetID := req.GetSpaceId(), req.GetDatasetId()
	if spaceID == "" || datasetID == "" {
		return "", "", errors.New("space_id and dataset_id are required")
	}
	for _, selector := range req.GetSelectors() {
		if selector == nil {
			return "", "", errors.New("time-series selector is required")
		}
		if selector.GetSpaceId() == "" || selector.GetDatasetId() == "" ||
			selector.GetSubjectId() == "" || selector.GetFreq() == "" {
			return "", "", errors.New("selector space_id, dataset_id, subject_id, and freq are required")
		}
		if selector.GetSpaceId() != spaceID || selector.GetDatasetId() != datasetID {
			return "", "", errors.New("one range read may reference only the request space/dataset")
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

func recordRowKey(key *pb.RecordKey) *pb.RowKey {
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: key.GetRecordId(), Version: key.GetVersion()}}}
}

func recordKeyFromRowKey(key *pb.RowKey) *pb.RecordKey {
	if key == nil || key.GetRecord() == nil {
		return nil
	}
	return &pb.RecordKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), RecordId: key.GetRecord().GetRecordId(), Version: key.GetRecord().GetVersion()}
}
