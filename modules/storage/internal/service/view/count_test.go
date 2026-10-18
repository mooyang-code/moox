package view

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type countingEngine struct {
	*queryEngine
	counted string
	spec    viewindex.QuerySpec
	counts  map[string]int64
}

func (e *countingEngine) CountBySubject(_ context.Context, indexID string, spec viewindex.QuerySpec) (map[string]int64, error) {
	e.counted, e.spec = indexID, spec
	return e.counts, nil
}

func TestCountTimeSeriesRowsCountsTheActiveIndex(t *testing.T) {
	engine := &countingEngine{queryEngine: &queryEngine{}, counts: map[string]int64{"ETH-USDT": 58, "BTC-USDT": 60}}
	svc, auth := queryTestService(engine, true)
	timeRange := &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T01:00:00Z"}
	selectors := []*pb.TimeSeriesSelector{
		{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"},
		{SpaceId: "space", DatasetId: "market", SubjectId: "ETH-USDT", Freq: "1m"},
	}
	rsp, err := svc.CountTimeSeriesRows(context.Background(), &pb.CountTimeSeriesViewRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "prices", Selectors: selectors, TimeRange: timeRange})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if engine.counted != "prices-index" || len(engine.spec.Selectors) != 2 || engine.spec.TimeRange.GetEndTime() != timeRange.GetEndTime() {
		t.Fatalf("counted=%s spec=%+v", engine.counted, engine.spec)
	}
	if len(rsp.GetCounts()) != 2 || rsp.GetCounts()[0].GetSubjectId() != "BTC-USDT" || rsp.GetCounts()[1].GetRows() != 58 {
		t.Fatalf("counts=%v", rsp.GetCounts())
	}

	for name, req := range map[string]*pb.CountTimeSeriesViewRowsReq{
		"no selectors":        {AuthInfo: auth, SpaceId: "space", ViewId: "prices", TimeRange: timeRange},
		"no time range":       {AuthInfo: auth, SpaceId: "space", ViewId: "prices", Selectors: selectors},
		"another dataset":     {AuthInfo: auth, SpaceId: "space", ViewId: "prices", TimeRange: timeRange, Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "other", SubjectId: "BTC-USDT", Freq: "1m"}}},
		"selector incomplete": {AuthInfo: auth, SpaceId: "space", ViewId: "prices", TimeRange: timeRange, Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT"}}},
	} {
		rejected, err := svc.CountTimeSeriesRows(context.Background(), req)
		if err != nil || rejected.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
			t.Fatalf("%s: rsp=%v err=%v", name, rejected, err)
		}
	}
	denied, err := svc.CountTimeSeriesRows(context.Background(), &pb.CountTimeSeriesViewRowsReq{AuthInfo: &pb.AuthInfo{AppId: "query-test", AppKey: "wrong"}, SpaceId: "space", ViewId: "prices", Selectors: selectors, TimeRange: timeRange})
	if err != nil || denied.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("denied rsp=%v err=%v", denied, err)
	}
}
