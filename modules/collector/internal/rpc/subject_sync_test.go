package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

type fakeSyncer struct {
	supported  map[string][]string
	filter     subjectsync.Filter
	attributes bool
	report     subjectsync.Report
	err        error
	called     bool
}

func (f *fakeSyncer) SupportedListers() map[string][]string { return f.supported }

func (f *fakeSyncer) SyncNow(_ context.Context, filter subjectsync.Filter, attributes bool) (subjectsync.Report, error) {
	f.called, f.filter, f.attributes = true, filter, attributes
	return f.report, f.err
}

func TestSyncSubjectsReturnsEveryResult(t *testing.T) {
	syncer := &fakeSyncer{report: subjectsync.Report{
		Tags: []subjectsync.TagResult{
			{SpaceID: "crypto", TagID: "binance_spot", Status: subjectsync.StatusSuccess, Subjects: 678},
			{SpaceID: "crypto", TagID: "binance_swap", Status: subjectsync.StatusFailed, Message: "HTTP status 302"},
		},
		Attributes: []subjectsync.AttributeResult{{SpaceID: "crypto", Status: subjectsync.StatusSuccess}},
	}}
	svc := &Service{}
	svc.SetSubjectSyncer(syncer)
	rsp, err := svc.SyncSubjects(context.Background(), &pb.SyncSubjectsReq{SpaceId: " crypto ", IncludeAttributes: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, subjectsync.Filter{SpaceID: "crypto"}, syncer.filter)
	require.True(t, syncer.attributes)
	require.Len(t, rsp.GetTags(), 2)
	require.EqualValues(t, 678, rsp.GetTags()[0].GetSubjects())
	require.Equal(t, "failed", rsp.GetTags()[1].GetStatus())
	require.Equal(t, "HTTP status 302", rsp.GetTags()[1].GetMessage())
	require.Len(t, rsp.GetAttributes(), 1)
}

func TestSyncSubjectsRejectsBadRequests(t *testing.T) {
	syncer := &fakeSyncer{}
	svc := &Service{}
	svc.SetSubjectSyncer(syncer)

	rsp, _ := svc.SyncSubjects(context.Background(), &pb.SyncSubjectsReq{TagId: "binance_spot"})
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "tag_id without space_id")
	require.False(t, syncer.called)

	rsp, _ = svc.SyncSubjects(context.Background(), &pb.SyncSubjectsReq{SpaceId: "crypto", TagId: "missing"})
	require.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode(), "a filter that selects nothing is not a success")

	rsp, _ = (&Service{}).SyncSubjects(context.Background(), &pb.SyncSubjectsReq{})
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode(), "no syncer wired")
}

func TestSyncSubjectsReportsBusyAsConflict(t *testing.T) {
	svc := &Service{}
	svc.SetSubjectSyncer(&fakeSyncer{err: subjectsync.ErrBusy})
	rsp, err := svc.SyncSubjects(context.Background(), &pb.SyncSubjectsReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())

	svc.SetSubjectSyncer(&fakeSyncer{err: errors.New("list tags: boom")})
	rsp, _ = svc.SyncSubjects(context.Background(), &pb.SyncSubjectsReq{})
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}

// 运行状态来自 Collector 自己的库：失败不会抹掉上一次成功时间，没有记录的标签不出现。
func TestListTagSyncStatusReadsCollectorRuns(t *testing.T) {
	service, db := newReregisterFixture(t)
	ctx := context.Background()
	ok := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	require.NoError(t, db.TagSyncRuns().Record(ctx, store.TagSyncRun{SpaceID: "crypto", TagID: "binance_spot", LastRunAt: ok, LastStatus: store.TagSyncSuccess}))
	require.NoError(t, db.TagSyncRuns().Record(ctx, store.TagSyncRun{SpaceID: "crypto", TagID: "binance_swap", LastRunAt: ok, LastStatus: store.TagSyncSuccess}))
	require.NoError(t, db.TagSyncRuns().Record(ctx, store.TagSyncRun{SpaceID: "crypto", TagID: "binance_swap", LastRunAt: ok.Add(time.Hour), LastStatus: store.TagSyncFailed, LastError: "HTTP 451"}))
	require.NoError(t, db.TagSyncRuns().Record(ctx, store.TagSyncRun{SpaceID: "stockcn", TagID: "cn_a_share", LastRunAt: ok, LastStatus: store.TagSyncFailed, LastError: "boom"}))

	rsp, err := service.ListTagSyncStatus(ctx, &pb.ListTagSyncStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetItems(), 2)
	spot, swap := rsp.GetItems()[0], rsp.GetItems()[1]
	require.Equal(t, "binance_spot", spot.GetTagId())
	require.Equal(t, "success", spot.GetLastStatus())
	require.Equal(t, "2026-10-27T09:00:00Z", spot.GetLastSuccessAt())
	require.Equal(t, "failed", swap.GetLastStatus())
	require.Equal(t, "HTTP 451", swap.GetLastError())
	require.Equal(t, "2026-10-27T10:00:00Z", swap.GetLastRunAt())
	require.Equal(t, "2026-10-27T09:00:00Z", swap.GetLastSuccessAt(), "失败不抹掉上一次成功时间")

	all, err := service.ListTagSyncStatus(ctx, &pb.ListTagSyncStatusReq{})
	require.NoError(t, err)
	require.Len(t, all.GetItems(), 3, "空间为空表示所有空间")
	require.Empty(t, all.GetItems()[2].GetLastSuccessAt(), "从未成功过")
}

func TestListSubjectListersSortsSourcesAndTypes(t *testing.T) {
	svc := &Service{}
	svc.SetSubjectSyncer(&fakeSyncer{supported: map[string][]string{"eastmoney": {"equity"}, "binance": {"spot", "swap"}}})
	rsp, err := svc.ListSubjectListers(context.Background(), &pb.ListSubjectListersReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetListers(), 2)
	require.Equal(t, "binance", rsp.GetListers()[0].GetSource())
	require.Equal(t, []string{"spot", "swap"}, rsp.GetListers()[0].GetMarketTypes())
	require.Equal(t, "eastmoney", rsp.GetListers()[1].GetSource())

	rsp, _ = (&Service{}).ListSubjectListers(context.Background(), &pb.ListSubjectListersReq{})
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode(), "未配置同步器时给出明确错误")
}
