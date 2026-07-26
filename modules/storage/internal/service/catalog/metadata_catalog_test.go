package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type activationMetadataStore struct {
	metadata.Store
	dataset      *pb.Dataset
	node         *pb.DataNode
	datasetErr   error
	commitCalls  int
	commitErr    error
	lastExpected uint64
	beforeCommit func()
}

func (s *activationMetadataStore) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	if s.datasetErr != nil {
		return nil, s.datasetErr
	}
	if s.dataset == nil {
		return nil, sql.ErrNoRows
	}
	return s.dataset, nil
}

func (s *activationMetadataStore) GetDataNode(context.Context, string) (*pb.DataNode, error) {
	if s.node == nil {
		return nil, errors.New("data node not found")
	}
	return s.node, nil
}

func (s *activationMetadataStore) CommitDatasetActivation(_ context.Context, _, _ string, expected uint64) (*pb.Dataset, error) {
	s.commitCalls++
	s.lastExpected = expected
	if s.beforeCommit != nil {
		s.beforeCommit()
	}
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	if s.dataset.GetRevision() != expected {
		return nil, errors.New("dataset revision conflict")
	}
	s.dataset.Status = "active"
	s.dataset.BindingLocked = true
	s.dataset.Revision++
	return s.dataset, nil
}

func newActivationService(t *testing.T, dataset *pb.Dataset, node *pb.DataNode, runtime *fakeNodeStateChecker) (*Service, *activationMetadataStore) {
	t.Helper()
	store := &activationMetadataStore{dataset: dataset, node: node}
	svc, err := NewMetadataService(store, nil, Options{AuthSecret: "secret", NodeStateChecker: runtime})
	require.NoError(t, err)
	return svc, store
}

func TestCheckDatasetActivationIsReadOnly(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, rsp.GetReady())
	require.Equal(t, uint64(7), rsp.GetDatasetRevision())
	require.Zero(t, store.commitCalls)
	require.Equal(t, activationCheckIDs, checkIDs(rsp.GetChecks()))
}

func TestActivateDatasetCommitsWithExpectedRevision(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint64(7), store.lastExpected)
	require.Equal(t, "active", rsp.GetDataset().GetStatus())
	require.True(t, rsp.GetDataset().GetBindingLocked())
	require.Equal(t, uint64(8), rsp.GetDataset().GetRevision())
}

func TestActivateDatasetReportsCommittedPublicationFailureAndRetryIsIdempotent(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	store := &activationMetadataStore{dataset: dataset, node: &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}}
	svc, err := NewMetadataService(store, &metacache.Store{}, Options{AuthSecret: "secret", NodeStateChecker: runtime})
	require.NoError(t, err)

	first, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, first.GetRetInfo().GetCode())
	require.Contains(t, first.GetRetInfo().GetMsg(), "publication is pending")
	require.Equal(t, "active", first.GetDataset().GetStatus())
	require.True(t, first.GetDataset().GetBindingLocked())
	require.Equal(t, uint64(8), first.GetDataset().GetRevision())

	retry, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, retry.GetRetInfo().GetCode())
	require.Equal(t, uint64(8), retry.GetDataset().GetRevision())
	require.Equal(t, 1, store.commitCalls)
}

func TestActivateDatasetCASConflictDoesNotChangeState(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)
	store.beforeCommit = func() { store.dataset.Revision++ }

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
	require.Equal(t, "disabled", store.dataset.GetStatus())
	require.False(t, store.dataset.GetBindingLocked())
	require.Equal(t, uint64(8), store.dataset.GetRevision(), "the concurrent metadata mutation is preserved")
}

func TestActivateDatasetActiveLockedRetryIgnoresStaleRevision(t *testing.T) {
	dataset := newActivationReader("active", "ip://127.0.0.1:19090").dataset
	dataset.BindingLocked = true
	dataset.Revision = 11
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "disabled", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 1})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint64(11), rsp.GetDataset().GetRevision())
	require.Zero(t, store.commitCalls)
	require.Zero(t, runtime.calls, "idempotent retry must not rerun readiness")
}

func TestDatasetActivationReadErrorsAreSafeAndDistinguishedFromMissing(t *testing.T) {
	internalErr := errors.New("sqlite: secret=do-not-leak")
	svc, _ := newActivationService(t, newActivationReader("disabled", "ip://127.0.0.1:19090").dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, &fakeNodeStateChecker{rsp: readyNodeState("node-a")})
	store := svc.metadata.(*activationMetadataStore)
	store.datasetErr = fmt.Errorf("wrapped metadata read: %w", internalErr)

	checkRsp, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, checkRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset metadata could not be read", checkRsp.GetRetInfo().GetMsg())
	require.NotContains(t, checkRsp.GetRetInfo().GetMsg(), "secret")

	activateRsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, activateRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset metadata could not be read", activateRsp.GetRetInfo().GetMsg())

	store.datasetErr = fmt.Errorf("wrapped missing Dataset: %w", sql.ErrNoRows)
	missingRsp, err := svc.CheckDatasetActivation(context.Background(), &pb.CheckDatasetActivationReq{SpaceId: "space-a", DatasetId: "dataset_a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, missingRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset not found", missingRsp.GetRetInfo().GetMsg())

	activateMissingRsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, activateMissingRsp.GetRetInfo().GetCode())
	require.Equal(t, "Dataset not found", activateMissingRsp.GetRetInfo().GetMsg())
}

func TestActivateDatasetLockedDisabledCanReactivate(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	dataset.BindingLocked = true
	dataset.Revision = 11
	runtime := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 11})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint64(12), rsp.GetDataset().GetRevision())
	require.True(t, rsp.GetDataset().GetBindingLocked())
	require.Equal(t, 1, store.commitCalls)
}

func TestActivateDatasetReadinessFailureDoesNotWrite(t *testing.T) {
	dataset := newActivationReader("disabled", "ip://127.0.0.1:19090").dataset
	runtime := &fakeNodeStateChecker{rsp: &pb.GetNodeStateRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_PERMISSION}, Status: "READY"}}
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 7})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Zero(t, store.commitCalls)
	require.Equal(t, "disabled", store.dataset.GetStatus())
}
