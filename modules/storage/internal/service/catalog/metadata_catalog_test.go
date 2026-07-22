package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type activationMetadataStore struct {
	metadata.Store
	dataset      *pb.Dataset
	node         *pb.DataNode
	commitCalls  int
	commitErr    error
	lastExpected uint64
	beforeCommit func()
}

func (s *activationMetadataStore) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	if s.dataset == nil {
		return nil, errors.New("dataset not found")
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
	svc, err := NewMetadataServiceWithNodeStateChecker(store, nil, "secret", runtime)
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
	svc, store := newActivationService(t, dataset, &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:19090"}, runtime)

	rsp, err := svc.ActivateDataset(context.Background(), &pb.ActivateDatasetReq{SpaceId: "space-a", DatasetId: "dataset_a", ExpectedRevision: 1})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, uint64(11), rsp.GetDataset().GetRevision())
	require.Zero(t, store.commitCalls)
	require.Equal(t, 1, runtime.calls, "idempotent retry still reruns readiness")
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
