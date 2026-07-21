package primarystorev2

import (
	"context"
	"errors"
	"sort"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) ScanTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	spaceID, datasetID, err := scanDataset(req.GetSpaceId(), req.GetDatasetId(), req.GetKeys())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	node, err := s.resolve(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := node.ScanFields(ctx, &pb.ScanFieldsReq{
		AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, TimeRange: req.GetTimeRange(),
		FieldIds: req.GetColumnNames(), Page: req.GetPage(), PageToken: req.GetPageToken(), Order: req.GetOrder(),
	})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo()}, nil
	}
	rows := make([]*pb.TimeSeriesRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		if row == nil || row.GetKey().GetTimeSeries() == nil {
			continue
		}
		rows = append(rows, &pb.TimeSeriesRow{Key: legacyTimeSeriesKey(row.GetKey()), Fields: row.GetFields(), Columns: legacyColumns(row.GetFields())})
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows, PageResult: rsp.GetPageResult(), NextPageToken: rsp.GetNextPageToken()}, nil
}

func (s *Service) ScanRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	if req == nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	spaceID, datasetID, err := scanRecordDataset(req.GetSpaceId(), req.GetDatasetId(), req.GetKeys())
	if err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	node, err := s.resolve(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.ReadRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := node.ScanFields(ctx, &pb.ScanFieldsReq{
		AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
		DataKind: pb.DataKind_DATA_KIND_RECORD, VersionRange: req.GetVersionRange(),
		FieldIds: req.GetColumnNames(), Page: req.GetPage(), PageToken: req.GetPageToken(), Order: req.GetOrder(),
	})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return &pb.ReadRecordRowsRsp{RetInfo: rsp.GetRetInfo()}, nil
	}
	rows := make([]*pb.RecordRow, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		if row == nil || row.GetKey().GetRecord() == nil {
			continue
		}
		rows = append(rows, &pb.RecordRow{Key: legacyRecordKey(row.GetKey()), Fields: row.GetFields(), Columns: legacyColumns(row.GetFields())})
	}
	return &pb.ReadRecordRowsRsp{RetInfo: rsp.GetRetInfo(), Rows: rows, PageResult: rsp.GetPageResult(), NextPageToken: rsp.GetNextPageToken()}, nil
}

func (s *Service) GetShardHeads(ctx context.Context, req *pb.GetShardHeadsReq) (*pb.GetShardHeadsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.GetShardHeadsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.GetShardHeadsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if req.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES && req.GetDataKind() != pb.DataKind_DATA_KIND_RECORD {
		return &pb.GetShardHeadsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("data_kind must be time_series or record"))}, nil
	}
	node, err := s.resolve(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return nil, err
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.GetShardHeadsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	latest := make(map[string]*pb.RowKey)
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := node.ScanFields(ctx, &pb.ScanFieldsReq{AuthInfo: auth, SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), DataKind: req.GetDataKind(), Page: &pb.Page{Page: pageNo, Size: 1000}})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return &pb.GetShardHeadsRsp{RetInfo: rsp.GetRetInfo()}, nil
		}
		for _, row := range rsp.GetRows() {
			key := row.GetKey()
			if key == nil {
				continue
			}
			group := key.GetSpaceId() + "\x00" + key.GetDatasetId()
			if ts := key.GetTimeSeries(); ts != nil {
				group += "\x00ts\x00" + ts.GetSubjectId() + "\x00" + ts.GetFreq()
				if current := latest[group]; current == nil || current.GetTimeSeries().GetDataTime() < ts.GetDataTime() {
					latest[group] = key
				}
			} else if record := key.GetRecord(); record != nil {
				group += "\x00record\x00" + record.GetRecordId()
				if current := latest[group]; current == nil || current.GetRecord().GetVersion() < record.GetVersion() {
					latest[group] = key
				}
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	keys := make([]*pb.RowKey, 0, len(latest))
	for _, key := range latest {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return rowKeyIdentity(keys[i]) < rowKeyIdentity(keys[j]) })
	return &pb.GetShardHeadsRsp{RetInfo: retinfo.Success("success"), Keys: keys}, nil
}

func scanDataset(spaceID, datasetID string, keys []*pb.TimeSeriesKey) (string, string, error) {
	if spaceID == "" || datasetID == "" {
		spaceID, datasetID, err := timeSeriesDataset(keys)
		if err != nil {
			return "", "", errors.New("space_id and dataset_id are required for a scan")
		}
		return spaceID, datasetID, nil
	}
	return spaceID, datasetID, nil
}

func scanRecordDataset(spaceID, datasetID string, keys []*pb.RecordKey) (string, string, error) {
	if spaceID == "" || datasetID == "" {
		spaceID, datasetID, err := recordDataset(keys)
		if err != nil {
			return "", "", errors.New("space_id and dataset_id are required for a scan")
		}
		return spaceID, datasetID, nil
	}
	return spaceID, datasetID, nil
}

var _ pb.PrimaryStoreScanService = (*Service)(nil)
