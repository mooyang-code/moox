package seeding

import (
	"context"
	"errors"
	"testing"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
)

type fakeLister struct {
	seeds []*collectorpb.HistorySeed
	err   error
	calls int
	only  bool
}

func (f *fakeLister) ListHistorySeeds(_ context.Context, request *collectorpb.ListHistorySeedsReq) (*collectorpb.ListHistorySeedsRsp, error) {
	f.calls++
	f.only = request.GetOnlyRunning()
	if f.err != nil {
		return nil, f.err
	}
	return &collectorpb.ListHistorySeedsRsp{Seeds: f.seeds}, nil
}

func resolvedWith(bar string, tasks map[string]input.TaskBinding) input.Resolved {
	return input.Resolved{Bar: bar, Tasks: tasks}
}

// 取到的补齐中数据集按“空间、数据集、频率”匹配；调不通沿用上一次；从没取到过按没有补齐。
func TestTrackerRefreshAndFallback(t *testing.T) {
	lister := &fakeLister{}
	tracker := New(lister, nil)
	if tracker.Active("stockus", "kline", "5m") {
		t.Fatal("从没取到过应按没有补齐")
	}
	lister.err = errors.New("调不通")
	if err := tracker.Refresh(context.Background()); err == nil || tracker.Active("stockus", "kline", "5m") {
		t.Fatalf("首次失败应返回错误且按没有补齐：%v", err)
	}
	lister.err = nil
	lister.seeds = []*collectorpb.HistorySeed{
		{SpaceId: "stockus", DatasetId: "kline", Frequency: "5m", Status: "running"},
		{SpaceId: "stockus", DatasetId: "done_ds", Frequency: "5m", Status: "done"},
	}
	if err := tracker.Refresh(context.Background()); err != nil || !lister.only {
		t.Fatalf("应只请求补齐中的：%v only=%t", err, lister.only)
	}
	if !tracker.Active("stockus", "kline", "5M") || tracker.Active("stockus", "kline", "1h") || tracker.Active("crypto", "kline", "5m") || tracker.Active("stockus", "done_ds", "5m") {
		t.Fatal("匹配应按空间、数据集、频率（忽略大小写），且只认 running")
	}
	lister.err = errors.New("调不通")
	if err := tracker.Refresh(context.Background()); err == nil || !tracker.Active("stockus", "kline", "5m") {
		t.Fatal("调不通应沿用上一次的结果")
	}
	lister.err, lister.seeds = nil, nil
	if err := tracker.Refresh(context.Background()); err != nil || tracker.Active("stockus", "kline", "5m") {
		t.Fatal("补齐结束后应放行")
	}
}

// 实例绑定的任务结果数据集或其源 K 线数据集任一在补齐中就算；nil 的 Tracker 视为没有补齐。
func TestTrackerBound(t *testing.T) {
	lister := &fakeLister{seeds: []*collectorpb.HistorySeed{{SpaceId: "stockus", DatasetId: "kline", Frequency: "5m", Status: "running"}}}
	tracker := New(lister, nil)
	if err := tracker.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	direct := resolvedWith("5m", map[string]input.TaskBinding{"equity": {DatasetID: "kline"}})
	viaFactor := resolvedWith("5m", map[string]input.TaskBinding{"equity": {DatasetID: "factor_result", SourceDatasetID: "kline"}})
	other := resolvedWith("5m", map[string]input.TaskBinding{"equity": {DatasetID: "factor_result", SourceDatasetID: "other"}})
	otherBar := resolvedWith("1h", map[string]input.TaskBinding{"equity": {DatasetID: "kline"}})
	if !tracker.Bound("stockus", direct) || !tracker.Bound("stockus", viaFactor) {
		t.Fatal("直接绑 K 线或经因子绑到补齐中的 K 线都应算补齐中")
	}
	if tracker.Bound("stockus", other) || tracker.Bound("stockus", otherBar) || tracker.Bound("crypto", direct) {
		t.Fatal("别的数据集、别的频率、别的空间不应算补齐中")
	}
	var none *Tracker
	if none.Bound("stockus", direct) {
		t.Fatal("nil 的 Tracker 应视为没有补齐")
	}
}
