package rpc

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobhistory"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/projection"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

func TestRetHelpers_ShouldMapCodes(t *testing.T) {
	ok := retOK()
	assert.Equal(t, pb.ErrorCode_SUCCESS, ok.GetCode())
	assert.Equal(t, "ok", ok.GetMsg())

	err := retErr(pb.ErrorCode_INVALID_PARAM, "bad")
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, err.GetCode())
	assert.Equal(t, "bad", err.GetMsg())
}

func TestRetFromError_ShouldMapKnownErrors(t *testing.T) {
	cases := []struct {
		err  error
		code pb.ErrorCode
		msg  string
	}{
		{store.ErrPollingNodeNotFound, pb.ErrorCode_NOT_FOUND, "cloud node not found"},
		{jobstate.ErrConflict, pb.ErrorCode_INVALID_PARAM, "conflict: job item state does not allow this operation"},
		{jobstate.ErrStaleAttempt, pb.ErrorCode_INVALID_PARAM, "conflict: job item attempt is stale"},
		{jobstate.ErrInactive, pb.ErrorCode_INVALID_PARAM, "conflict: job item is not running"},
		{jobstate.ErrNotFound, pb.ErrorCode_NOT_FOUND, "job item not found"},
		{jobstate.ErrInvalid, pb.ErrorCode_INVALID_PARAM, "invalid job item"},
		{gorm.ErrRecordNotFound, pb.ErrorCode_NOT_FOUND, "resource not found"},
		{errors.New("boom"), pb.ErrorCode_INNER_ERR, "internal error"},
	}
	for _, tc := range cases {
		got := retFromError(tc.err)
		assert.Equal(t, tc.code, got.GetCode())
		assert.Equal(t, tc.msg, got.GetMsg())
	}
}

func TestNew_ShouldApplyOptions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	mgr, err := store.Open(&config.DatabaseConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	queue := &fakeExecutionQueue{}
	state := &fakeJobStateStore{}
	history := jobhistory.NewStore(jobhistory.StoreOptions{Dir: t.TempDir()})
	sink := projection.NewHeartbeatBuffer(mgr.Catalog(), projection.HeartbeatBufferOptions{MaxKeys: 4})
	t.Cleanup(func() { _ = sink.Close(context.Background()) })

	svc := New(mgr,
		WithExecutionQueue(queue),
		WithJobStateStore(state),
		WithJobHistoryStore(history),
		WithHeartbeatSink(sink),
	)
	require.NotNil(t, svc)
	assert.NotNil(t, svc.catalog)
	assert.Equal(t, queue, svc.executionQueue)
	assert.Equal(t, state, svc.jobState)
	assert.Equal(t, history, svc.history)
	assert.Equal(t, sink, svc.heartbeatSink)
	assert.NotNil(t, svc.scfClientFactory)

	account := store.CloudAccount{SecretID: "id", SecretKey: "key"}
	assert.NotNil(t, svc.scfClientFactory(account))
}

func TestListCloudRegions_ShouldReturnStaticCatalog(t *testing.T) {
	svc := &Service{catalog: newCatalogForAccountTests(t)}
	rsp, err := svc.ListCloudRegions(context.Background(), &pb.ListCloudRegionsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetRegions(), 4)
	assert.Equal(t, int64(4), rsp.GetTotal())
}

type fakeExecutionQueue struct{}

func (f *fakeExecutionQueue) Publish(context.Context, *pb.JobItem) (*jobqueue.PublishResult, error) {
	return &jobqueue.PublishResult{}, nil
}
func (f *fakeExecutionQueue) Fetch(context.Context, jobqueue.FetchRequest) ([]jobqueue.Delivery, error) {
	return nil, nil
}
func (f *fakeExecutionQueue) Ack(context.Context, string) error                { return nil }
func (f *fakeExecutionQueue) Nak(context.Context, string, time.Duration) error { return nil }
func (f *fakeExecutionQueue) Term(context.Context, string) error               { return nil }
func (f *fakeExecutionQueue) InProgress(context.Context, string) error         { return nil }
func (f *fakeExecutionQueue) Close() error                                     { return nil }

type fakeJobStateStore struct{}

func (f *fakeJobStateStore) CreatePending(context.Context, *pb.JobItem, jobstate.QueueMeta) (*jobstate.CreateResult, error) {
	return nil, nil
}
func (f *fakeJobStateStore) MarkPublished(context.Context, string, string, jobstate.QueueMeta) error {
	return nil
}
func (f *fakeJobStateStore) MarkEnqueueFailed(context.Context, string, string, string) error {
	return nil
}
func (f *fakeJobStateStore) Get(context.Context, string, string) (*jobstate.State, error) {
	return nil, nil
}
func (f *fakeJobStateStore) TryMarkRunning(context.Context, jobstate.RunningRequest) (bool, jobstate.RunningState, error) {
	return false, jobstate.RunningState{}, nil
}
func (f *fakeJobStateStore) MarkCanceled(context.Context, string, string, string) error { return nil }
func (f *fakeJobStateStore) ClearCancelDirective(context.Context, string, string, int32) error {
	return nil
}
func (f *fakeJobStateStore) MarkReported(context.Context, jobstate.ReportEvent) (*jobstate.State, error) {
	return nil, nil
}
func (f *fakeJobStateStore) MarkHistorySynced(context.Context, string, string) error { return nil }
func (f *fakeJobStateStore) List(context.Context, *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *commonpb.PageResult, error) {
	return nil, nil, nil
}
func (f *fakeJobStateStore) ListAttempts(context.Context, *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error) {
	return nil, nil
}
func (f *fakeJobStateStore) ListCancelDirectives(context.Context, string, string, int) ([]*pb.ControlDirective, error) {
	return nil, nil
}

func TestCloudNodeProtoContractIncludesQueueDirectives(t *testing.T) {
	if pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED.Number() == 0 {
		t.Fatalf("JOB_ITEM_STATUS_ENQUEUE_FAILED must be non-zero")
	}
	if pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_CANCELED.Number() == 0 {
		t.Fatalf("JOB_ITEM_REPORT_STATUS_CANCELED must be non-zero")
	}
	rsp := &pb.ReportHeartbeatRsp{Directives: []*pb.ControlDirective{{
		Type:      pb.ControlDirectiveType_CONTROL_DIRECTIVE_CANCEL,
		JobItemId: "ji-1",
		AttemptNo: 1,
		Reason:    "test",
	}}}
	if len(rsp.GetDirectives()) != 1 || rsp.GetDirectives()[0].GetType() != pb.ControlDirectiveType_CONTROL_DIRECTIVE_CANCEL {
		t.Fatalf("heartbeat directives not available: %+v", rsp)
	}
}
