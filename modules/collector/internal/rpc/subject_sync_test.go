package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

type fakeSyncer struct {
	filter     subjectsync.Filter
	attributes bool
	report     subjectsync.Report
	err        error
	called     bool
}

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
