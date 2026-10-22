package primarystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 区间读取与计数对所有调用方的限制。
const (
	maxReadSelectors   = 100
	maxCountSelectors  = 2000
	maxRangePageSize   = 2000
	maxLatestPageSize  = 20000
	maxLatestPerSeries = 10000
)

// ReadTimeSeriesRows 读取时序事实：所有调用方的请求都由 DataNode（Pebble）回答，结果一致。
// 两种形态二选一：time_range（左闭右开，起止都必须给出）或 latest_per_series（每个序列最近 N 根）。
// 不带 selector 表示数据集的全部标的。分页只用 page.size 与 page.cursor，不能跳页。
func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	ctx = s.requestContext(ctx)
	if err := validateTimeSeriesRead(ctx, req); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	node, err := s.rangeNode(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return nil, err
	}
	if node == nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode range runtime is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	return node.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), Selectors: req.GetSelectors(),
		ColumnNames: req.GetColumnNames(), TimeRange: req.GetTimeRange(), LatestPerSeries: req.GetLatestPerSeries(), Page: req.GetPage(),
	})
}

// CountTimeSeriesRows 按标的统计时间范围内已存在的时间点数，由 DataNode 回答，只回传计数。
func (s *Service) CountTimeSeriesRows(ctx context.Context, req *pb.CountTimeSeriesRowsReq) (*pb.CountTimeSeriesRowsRsp, error) {
	if req == nil || len(req.GetSelectors()) == 0 || req.GetTimeRange() == nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("selectors and time_range are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	ctx = s.requestContext(ctx)
	if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if err := validateSelectors(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSelectors(), maxCountSelectors, false); err != nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if _, _, err := parseRequiredRange(req.GetTimeRange()); err != nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	node, err := s.rangeNode(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return nil, err
	}
	if node == nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode range runtime is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	return node.CountTimeSeriesRows(ctx, &pb.CountTimeSeriesRowsReq{AuthInfo: auth, SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), Selectors: req.GetSelectors(), TimeRange: req.GetTimeRange()})
}

// ReadDatasetChanges 读取数据集所在 DataNode 的持久化行变更日志：from_head 取基线位点，否则从游标之后按序读。
// 游标身份对不上或早于日志保留起点时响应里 gap 为真，调用方走对账。
func (s *Service) ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ReadDatasetChangesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if !req.GetFromHead() && req.GetCursor() == nil {
		return &pb.ReadDatasetChangesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("cursor is required unless from_head is set"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.ReadDatasetChangesRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	ctx = s.requestContext(ctx)
	node, err := s.rangeNode(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return nil, err
	}
	if node == nil {
		return &pb.ReadDatasetChangesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode range runtime is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.ReadDatasetChangesRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	return node.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{
		AuthInfo: auth, SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(),
		Cursor: req.GetCursor(), FromHead: req.GetFromHead(), Limit: req.GetLimit(),
	})
}

func (s *Service) rangeNode(ctx context.Context, spaceID, datasetID string) (rangeDataNodeClient, error) {
	node, err := s.resolve(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	ranged, _ := node.(rangeDataNodeClient)
	return ranged, nil
}

// validateTimeSeriesRead 是所有调用方共用的请求校验。
func validateTimeSeriesRead(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) error {
	if req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return errors.New("space_id and dataset_id are required")
	}
	if len(req.GetColumnNames()) == 0 {
		return errors.New("column_names are required")
	}
	if req.GetPage().GetPage() != 0 {
		return errors.New("page numbers are not supported; use page.cursor")
	}
	if err := validateSelectors(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSelectors(), maxReadSelectors, false); err != nil {
		return err
	}
	latest := req.GetLatestPerSeries()
	if (latest > 0) == (req.GetTimeRange() != nil) {
		return errors.New("provide exactly one of time_range or latest_per_series")
	}
	size := req.GetPage().GetSize()
	if latest > 0 {
		if latest > maxLatestPerSeries {
			return fmt.Errorf("latest_per_series must be between 1 and %d", maxLatestPerSeries)
		}
		if size > maxLatestPageSize {
			return fmt.Errorf("page.size must be at most %d for latest_per_series reads", maxLatestPageSize)
		}
		return nil
	}
	if size > maxRangePageSize {
		return fmt.Errorf("page.size must be between 1 and %d for range reads", maxRangePageSize)
	}
	_, _, err := parseRequiredRange(req.GetTimeRange())
	return err
}

func validateSelectors(ctx context.Context, spaceID, datasetID string, selectors []*pb.TimeSeriesSelector, limit int, required bool) error {
	if required && len(selectors) == 0 {
		return errors.New("selectors are required")
	}
	if len(selectors) > limit {
		return fmt.Errorf("selectors exceed the limit of %d", limit)
	}
	freq := ""
	if snapshot := metadata.RequestSnapshotFromContext(ctx); snapshot != nil {
		if dataset, ok := snapshot.GetDataset(spaceID, datasetID); ok && dataset != nil {
			freq = dataset.GetFreq()
		}
	}
	for _, selector := range selectors {
		if selector == nil || selector.GetSubjectId() == "" || selector.GetFreq() == "" {
			return errors.New("selector subject_id and freq are required")
		}
		if (selector.GetSpaceId() != "" && selector.GetSpaceId() != spaceID) || (selector.GetDatasetId() != "" && selector.GetDatasetId() != datasetID) {
			return errors.New("one read may reference only the request space/dataset")
		}
		if freq != "" && selector.GetFreq() != freq {
			return fmt.Errorf("selector freq %q does not match the dataset frequency %q", selector.GetFreq(), freq)
		}
	}
	return nil
}

func parseRequiredRange(tr *pb.TimeRange) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339Nano, tr.GetStartTime())
	if err != nil {
		return start, start, errors.New("time_range.start_time must be RFC3339")
	}
	end, err := time.Parse(time.RFC3339Nano, tr.GetEndTime())
	if err != nil || !start.Before(end) {
		return start, end, errors.New("time_range.end_time must be RFC3339 and after start_time")
	}
	return start, end, nil
}
