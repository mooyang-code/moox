package primarystore

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// These RPC handlers preserve the public PrimaryStore row contract while
// exact reads use the field API and range reads use the registered View.
func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil || (len(req.GetSelectors()) == 0 && len(req.GetKeys()) == 0 && req.GetAuthInfo().GetAppId() != "storage-view") {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("selectors or keys are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if strings.EqualFold(strings.TrimSpace(req.GetAuthInfo().GetAppId()), mooxSkillAppID) {
		if err := validateMooxSkillReadRequest(req); err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
		}
	}
	if req.GetAuthInfo().GetAppId() != "storage-view" {
		if _, _, err := timeSeriesDataset(req); err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
	} else if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	// Storage View uses the same PrimaryStore endpoint for historical rebuilds,
	// but must bypass the normal range-read -> View resolver or it would recurse
	// into the empty/new View. The internal app identity is authenticated by the
	// PrimaryStore caller and the DataNode history runtime performs the scan.
	if req.GetAuthInfo().GetAppId() == "storage-view" && len(req.GetKeys()) == 0 {
		return s.readHistoricalTimeSeriesRows(ctx, req)
	}
	if len(req.GetKeys()) == 0 {
		return s.readTimeSeriesView(ctx, req)
	}
	if len(req.GetColumnNames()) == 0 || req.GetTimeRange() != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("exact key reads require column_names and no time_range"))}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		keys = append(keys, timeSeriesRowKey(key))
	}
	rsp, err := s.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: req.GetAuthInfo(), Keys: keys, FieldIds: req.GetColumnNames()})
	if err != nil {
		return nil, err
	}
	existing := existingRowIdentities(rsp.GetExistingKeys())
	rows := make([]*pb.TimeSeriesRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		if !existing[rowKeyIdentity(row.GetKey())] {
			continue
		}
		rows = append(rows, &pb.TimeSeriesRow{Key: timeSeriesKeyFromRowKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows}, nil
}

func validateMooxSkillReadRequest(req *pb.ReadTimeSeriesRowsReq) error {
	if req.GetSpaceId() != "crypto_market" || req.GetDatasetId() != "binance_spot_kline_1m" {
		return errors.New("moox-skill read scope is invalid")
	}
	if len(req.GetKeys()) > 0 || req.GetOrder() != pb.SortOrder_SORT_ORDER_DESC {
		return errors.New("moox-skill read shape is invalid")
	}
	if page := req.GetPage(); page != nil && page.GetSize() > 1000 {
		return errors.New("moox-skill page size is too large")
	}
	if len(req.GetSelectors()) == 0 {
		return errors.New("moox-skill selectors are required")
	}
	for _, selector := range req.GetSelectors() {
		if selector == nil || selector.GetSpaceId() != "crypto_market" || selector.GetDatasetId() != "binance_spot_kline_1m" || selector.GetFreq() != "1m" || selector.GetSeriesTag() != "venue:binance" {
			return errors.New("moox-skill selector scope is invalid")
		}
	}
	return nil
}

func (s *Service) readHistoricalTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	node, err := s.resolve(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return nil, err
	}
	history, ok := node.(historyDataNodeClient)
	if !ok {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode history runtime is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	clone := &pb.ReadTimeSeriesRowsReq{
		AuthInfo: auth, Selectors: req.GetSelectors(), TimeRange: req.GetTimeRange(), Order: req.GetOrder(),
		ColumnNames: req.GetColumnNames(), Page: req.GetPage(), SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), AfterKey: req.GetAfterKey(),
	}
	return history.ReadTimeSeriesRows(ctx, clone)
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
	existing := existingRowIdentities(rsp.GetExistingKeys())
	rows := make([]*pb.RecordRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		if !existing[rowKeyIdentity(row.GetKey())] {
			continue
		}
		rows = append(rows, &pb.RecordRow{Key: recordKeyFromRowKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.ReadRecordRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows}, nil
}

func existingRowIdentities(keys []*pb.RowKey) map[string]bool {
	existing := make(map[string]bool, len(keys))
	for _, key := range keys {
		existing[rowKeyIdentity(key)] = true
	}
	return existing
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
	if (len(req.GetSelectors()) == 0) == (len(req.GetKeys()) == 0) {
		return "", "", errors.New("provide exactly one of selectors or keys")
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
	for _, key := range req.GetKeys() {
		if key == nil || key.GetSpaceId() == "" || key.GetDatasetId() == "" ||
			key.GetSubjectId() == "" || key.GetFreq() == "" || key.GetDataTime() == "" {
			return "", "", errors.New("key space_id, dataset_id, subject_id, freq, and data_time are required")
		}
		if key.GetSpaceId() != spaceID || key.GetDatasetId() != datasetID {
			return "", "", errors.New("one exact read may reference only the request space/dataset")
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
	return &pb.TimeSeriesKey{
		SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
		SubjectId: key.GetTimeSeries().GetSubjectId(), Freq: key.GetTimeSeries().GetFreq(),
		DataTime: key.GetTimeSeries().GetDataTime(), SeriesTag: key.GetTimeSeries().GetSeriesTag(),
	}
}

func timeSeriesRowKey(key *pb.TimeSeriesKey) *pb.RowKey {
	return &pb.RowKey{
		SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: key.GetSubjectId(), Freq: key.GetFreq(),
			DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag(),
		}},
	}
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
