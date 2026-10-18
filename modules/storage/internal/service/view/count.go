package view

import (
	"context"
	"errors"
	"sort"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// maxCountSelectors 是一次计数允许的标的数：只回传计数，可以比读行的批次大得多。
const maxCountSelectors = 2000

// CountTimeSeriesRows 按标的统计 active View 索引里时间范围内的行数（同一时间点多个 series_tag 只计一次），只回传计数。
// 计数只用来判断“够不够”，不要求与并发写入严格一致，所以不做读前读后的索引版本比对。
func (s *Service) CountTimeSeriesRows(ctx context.Context, req *pb.CountTimeSeriesViewRowsReq) (*pb.CountTimeSeriesRowsRsp, error) {
	reject := func(code pb.ErrorCode, err error) (*pb.CountTimeSeriesRowsRsp, error) {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(code, err)}, nil
	}
	if req == nil || req.GetViewId() == "" {
		return reject(pb.ErrorCode_INVALID_PARAM, errors.New("view_id is required"))
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return reject(pb.ErrorCode_NO_PERMISSION, err)
	}
	if len(req.GetSelectors()) == 0 || len(req.GetSelectors()) > maxCountSelectors || req.GetTimeRange() == nil {
		return reject(pb.ErrorCode_INVALID_PARAM, errors.New("count requires 1 to 2000 selectors and a time range"))
	}
	indexID, _ := s.activeIndex(req.GetSpaceId(), req.GetViewId())
	selectors, err := s.timeSeriesSelectors(indexID, &pb.QueryTimeSeriesRowsReq{SpaceId: req.GetSpaceId(), ViewId: req.GetViewId(), Selectors: req.GetSelectors()})
	if err != nil {
		return reject(pb.ErrorCode_INVALID_PARAM, err)
	}
	engine, err := s.engineFor(indexID)
	if err != nil {
		return reject(queryErrorCode(err), err)
	}
	counter, ok := engine.(viewindex.RowCounter)
	if !ok {
		return reject(pb.ErrorCode_INNER_ERR, errors.New("view engine cannot count rows"))
	}
	counts, err := counter.CountBySubject(ctx, indexID, viewindex.QuerySpec{Selectors: selectors, TimeRange: req.GetTimeRange()})
	if err != nil {
		return reject(queryErrorCode(err), err)
	}
	rsp := &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Counts: make([]*pb.SubjectRowCount, 0, len(counts))}
	for subject, count := range counts {
		rsp.Counts = append(rsp.Counts, &pb.SubjectRowCount{SubjectId: subject, Rows: uint32(count)})
	}
	sort.Slice(rsp.Counts, func(i, j int) bool { return rsp.Counts[i].GetSubjectId() < rsp.Counts[j].GetSubjectId() })
	return rsp, nil
}
