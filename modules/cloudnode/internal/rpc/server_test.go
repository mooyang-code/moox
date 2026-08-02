package rpc

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobhistory"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
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
	svc := New(mgr,
		WithExecutionQueue(queue),
		WithJobStateStore(state),
		WithJobHistoryStore(history),
	)
	require.NotNil(t, svc)
	assert.NotNil(t, svc.catalog)
	assert.Equal(t, queue, svc.executionQueue)
	assert.Equal(t, state, svc.jobState)
	assert.Equal(t, history, svc.history)
	assert.NotNil(t, svc.scfClientFactory)

	assert.NotNil(t, svc.scfClientFactory(cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}))
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

type fakeCredentialResolver struct {
	credential cloudcredential.TencentCredential
	err        error
}

func (r fakeCredentialResolver) Resolve(context.Context, store.CloudAccount) (cloudcredential.TencentCredential, error) {
	return r.credential, r.err
}

func (f *fakeExecutionQueue) EnsureJobExecutionQueue(context.Context, cloudjobqueue.Identity) error {
	return nil
}
func (f *fakeExecutionQueue) Publish(context.Context, *pb.JobItem) error { return nil }
func (f *fakeExecutionQueue) Close() error                               { return nil }

type fakeJobStateStore struct{}

func (f *fakeJobStateStore) CreatePending(context.Context, *pb.JobItem) (*jobstate.CreateResult, error) {
	return nil, nil
}
func (f *fakeJobStateStore) MarkEnqueueFailed(context.Context, string, string, string) error {
	return nil
}
func (f *fakeJobStateStore) Get(context.Context, string, string) (*jobstate.State, error) {
	return nil, nil
}
func (f *fakeJobStateStore) MarkReported(context.Context, jobstate.ReportEvent) (*jobstate.State, bool, error) {
	return nil, false, nil
}
func (f *fakeJobStateStore) List(context.Context, *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *commonpb.PageResult, error) {
	return nil, nil, nil
}
func TestCloudNodeProtoContractIncludesEnqueueFailure(t *testing.T) {
	if pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED.Number() == 0 {
		t.Fatalf("JOB_ITEM_STATUS_ENQUEUE_FAILED must be non-zero")
	}
}
