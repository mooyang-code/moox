package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

// QueryPipelineArrivalHistory 返回一条数据链路某一天各环节逐根 K 线的到达延迟，供数据链路的曲线视图使用。
func (s *Service) QueryPipelineArrivalHistory(ctx context.Context, req *monitorpb.QueryPipelineArrivalHistoryReq) (*monitorpb.QueryPipelineArrivalHistoryRsp, error) {
	if s.healthView == nil || s.healthView.Facts == nil {
		return &monitorpb.QueryPipelineArrivalHistoryRsp{RetInfo: inner(errors.New("到达历史不可用"))}, nil
	}
	history, err := s.healthView.Facts.ArrivalHistory(ctx, observability.ArrivalHistoryQuery{
		SpaceID: req.GetSpaceId(), DatasetID: req.GetDatasetId(), Freq: req.GetFreq(), Day: req.GetDay(), TaskID: req.GetTaskId(),
	})
	if errors.Is(err, observability.ErrInvalidArrivalQuery) {
		return &monitorpb.QueryPipelineArrivalHistoryRsp{RetInfo: invalid(err)}, nil
	}
	if err != nil {
		return &monitorpb.QueryPipelineArrivalHistoryRsp{RetInfo: inner(err)}, nil
	}
	rsp := &monitorpb.QueryPipelineArrivalHistoryRsp{
		RetInfo: success(), Timezone: history.Timezone, Day: history.Day,
		StartAt: history.Start.UTC().Format(time.RFC3339), EndAt: history.End.UTC().Format(time.RFC3339),
		RetentionTruncated: history.RetentionTruncated, Truncated: history.Truncated,
	}
	for _, task := range history.Tasks {
		rsp.Tasks = append(rsp.Tasks, &monitorpb.ArrivalTask{TaskId: task.TaskID, Name: task.Name, ResultDatasetId: task.ResultDatasetID})
	}
	for _, series := range history.Series {
		out := &monitorpb.ArrivalHistorySeries{
			Stage: series.Stage, Producer: series.Producer, DatasetId: series.DatasetID, PrimaryDatasetId: series.PrimaryDatasetID,
			LatenessLimitSeconds: series.LatenessLimitSeconds, Points: make([]*monitorpb.ArrivalHistoryPoint, 0, len(series.Points)),
		}
		for _, point := range series.Points {
			out.Points = append(out.Points, &monitorpb.ArrivalHistoryPoint{
				CloseAt: point.Close.UTC().Format(time.RFC3339), DelaySeconds: point.DelaySeconds, SkippedBars: int32(point.SkippedBars),
			})
		}
		rsp.Series = append(rsp.Series, out)
	}
	return rsp, nil
}
