package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fakeMetadataDeploymentClient struct {
	nodeState     *storagepb.GetNodeStateRsp
	nodeStateReq  *storagepb.GetNodeStateReq
	registerReq   *storagepb.RegisterDataNodeReq
	listRequests  []*storagepb.ListDatasetsReq
	checkRequests []*storagepb.CheckDatasetActivationReq
	activateReqs  []*storagepb.ActivateDatasetReq
	listResponses []*storagepb.ListDatasetsRsp
	check         map[string]*storagepb.CheckDatasetActivationRsp
	activate      map[string]*storagepb.ActivateDatasetRsp
}

func (f *fakeMetadataDeploymentClient) GetNodeState(_ context.Context, req *storagepb.GetNodeStateReq) (*storagepb.GetNodeStateRsp, error) {
	f.nodeStateReq = req
	if f.nodeState == nil {
		return &storagepb.GetNodeStateRsp{
			RetInfo: storageSuccess(),
			NodeId:  req.GetNodeId(),
			Status:  "READY",
		}, nil
	}
	return f.nodeState, nil
}

func (f *fakeMetadataDeploymentClient) RegisterDataNode(_ context.Context, req *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error) {
	f.registerReq = req
	return &storagepb.RegisterDataNodeRsp{
		RetInfo: storageSuccess(),
		Node:    &storagepb.DataNode{NodeId: req.GetNodeId(), ServiceTarget: req.GetServiceTarget(), Name: req.GetInitialName(), Status: "active"},
	}, nil
}

func (f *fakeMetadataDeploymentClient) ListDatasets(_ context.Context, req *storagepb.ListDatasetsReq) (*storagepb.ListDatasetsRsp, error) {
	f.listRequests = append(f.listRequests, req)
	index := len(f.listRequests) - 1
	if index >= len(f.listResponses) {
		return &storagepb.ListDatasetsRsp{RetInfo: storageSuccess()}, nil
	}
	return f.listResponses[index], nil
}

func (f *fakeMetadataDeploymentClient) CheckDatasetActivation(_ context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	f.checkRequests = append(f.checkRequests, req)
	return f.check[req.GetSpaceId()+"/"+req.GetDatasetId()], nil
}

func (f *fakeMetadataDeploymentClient) ActivateDataset(_ context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	f.activateReqs = append(f.activateReqs, req)
	return f.activate[req.GetSpaceId()+"/"+req.GetDatasetId()], nil
}

func storageSuccess() *storagepb.RetInfo {
	return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
}

func storageFailure(code storagepb.ErrorCode) *storagepb.RetInfo {
	return &storagepb.RetInfo{Code: code}
}

func TestResolveSeedPathRequiresExplicitFlagOrEnvironment(t *testing.T) {
	t.Setenv("STORAGE_SEED_FILE", "")
	if got := resolveSeedPath("", "config/storage.yaml"); got != "" {
		t.Fatalf("implicit seed path = %q, want empty", got)
	}
	if got := resolveSeedPath("bundle/metadata.yaml", "config/storage.yaml"); got != "bundle/metadata.yaml" {
		t.Fatalf("explicit seed path = %q", got)
	}
	t.Setenv("STORAGE_SEED_FILE", "bundle/from-env.yaml")
	if got := resolveSeedPath("", "config/storage.yaml"); got != "bundle/from-env.yaml" {
		t.Fatalf("environment seed path = %q", got)
	}
}

func withDeploymentSecret(t *testing.T) {
	t.Helper()
	previous, ok := os.LookupEnv("MOOX_STORAGE_NODE_AUTH_SECRET")
	if err := os.Setenv("MOOX_STORAGE_NODE_AUTH_SECRET", "test-storage-node-secret"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv("MOOX_STORAGE_NODE_AUTH_SECRET", previous)
		} else {
			_ = os.Unsetenv("MOOX_STORAGE_NODE_AUTH_SECRET")
		}
	})
}

func TestRegisterNodeSendsDeploymentHMACAndSanitizedOutput(t *testing.T) {
	withDeploymentSecret(t)
	fake := &fakeMetadataDeploymentClient{}
	var stdout bytes.Buffer
	err := runRegisterNode(context.Background(), []string{
		"--metadata-target", "ip://127.0.0.1:20100",
		"--node-id", "storage-node-0",
		"--service-target", "ip://127.0.0.1:20107",
		"--name", "数据节点",
	}, &stdout, func(string) metadataDeploymentClient { return fake }, func(string) dataNodeRuntimeClient { return fake })
	if err != nil {
		t.Fatal(err)
	}
	if fake.registerReq == nil {
		t.Fatal("register request was not sent")
	}
	if fake.nodeStateReq == nil || fake.nodeStateReq.GetNodeId() != "storage-node-0" {
		t.Fatalf("unexpected DataNode readiness request: %v", fake.nodeStateReq)
	}
	if got := fake.registerReq.GetAuthInfo().GetAppId(); got != storageDeployerAppID {
		t.Fatalf("app id = %q, want %q", got, storageDeployerAppID)
	}
	if fake.registerReq.GetAuthInfo().GetAppKey() == "" {
		t.Fatal("service HMAC is empty")
	}
	if fake.registerReq.GetAuthInfo().GetAppKey() == "test-storage-node-secret" {
		t.Fatal("service HMAC must not be the raw secret")
	}
	var output operationResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if output.Action != "register-node" || output.Status != "ok" {
		t.Fatalf("unexpected operation output: %s", stdout.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("test-storage-node-secret")) || bytes.Contains(stdout.Bytes(), []byte("app_key")) {
		t.Fatalf("sanitized output leaked credentials: %s", stdout.String())
	}
}

func TestActivateDatasetsChecksDisabledRowsAndUsesObservedRevision(t *testing.T) {
	withDeploymentSecret(t)
	fake := &fakeMetadataDeploymentClient{
		listResponses: []*storagepb.ListDatasetsRsp{{
			RetInfo: storageSuccess(),
			Datasets: []*storagepb.Dataset{
				{SpaceId: "space", DatasetId: "active", Status: "active", Revision: 1},
				{SpaceId: "space", DatasetId: "not-ready", Status: "disabled", Revision: 2},
				{SpaceId: "space", DatasetId: "ready", Status: "disabled", Revision: 3},
			},
			PageResult: &storagepb.PageResult{HasMore: false},
		}},
		check: map[string]*storagepb.CheckDatasetActivationRsp{
			"space/not-ready": {RetInfo: storageSuccess(), DatasetRevision: 8, Ready: false, Checks: []*storagepb.DatasetActivationCheck{{CheckId: "node", Ready: false, Summary: "node is not ready"}}},
			"space/ready":     {RetInfo: storageSuccess(), DatasetRevision: 9, Ready: true, Checks: []*storagepb.DatasetActivationCheck{{CheckId: "node", Ready: true, Summary: "ready"}}},
		},
		activate: map[string]*storagepb.ActivateDatasetRsp{
			"space/ready": {RetInfo: storageSuccess(), Dataset: &storagepb.Dataset{SpaceId: "space", DatasetId: "ready", Status: "active", Revision: 10}},
		},
	}
	var stdout bytes.Buffer
	err := runActivateDatasets(context.Background(), []string{"--metadata-target", "ip://127.0.0.1:20100"}, &stdout, func(string) metadataDeploymentClient { return fake })
	if err == nil {
		t.Fatal("not-ready Dataset must make activation nonzero")
	}
	if len(fake.checkRequests) != 2 || len(fake.activateReqs) != 1 {
		t.Fatalf("check calls = %d, activate calls = %d", len(fake.checkRequests), len(fake.activateReqs))
	}
	if got := fake.activateReqs[0].GetExpectedRevision(); got != 9 {
		t.Fatalf("activate revision = %d, want observed revision 9", got)
	}
	if fake.activateReqs[0].GetDatasetId() != "ready" {
		t.Fatalf("activated unexpected Dataset %q", fake.activateReqs[0].GetDatasetId())
	}
	if bytes.Contains(stdout.Bytes(), []byte("test-storage-node-secret")) {
		t.Fatalf("activation output leaked credentials: %s", stdout.String())
	}
}

func TestActivateDatasetsWithNoDisabledRowsSucceeds(t *testing.T) {
	withDeploymentSecret(t)
	fake := &fakeMetadataDeploymentClient{
		listResponses: []*storagepb.ListDatasetsRsp{{
			RetInfo:    storageSuccess(),
			Datasets:   []*storagepb.Dataset{{SpaceId: "space", DatasetId: "active", Status: "active", Revision: 4}},
			PageResult: &storagepb.PageResult{HasMore: false},
		}},
	}
	var stdout bytes.Buffer
	if err := runActivateDatasets(context.Background(), []string{"--metadata-target", "ip://127.0.0.1:20100"}, &stdout, func(string) metadataDeploymentClient { return fake }); err != nil {
		t.Fatal(err)
	}
	if len(fake.checkRequests) != 0 || len(fake.activateReqs) != 0 {
		t.Fatalf("zero disabled rows must not call check/activate: checks=%d activates=%d", len(fake.checkRequests), len(fake.activateReqs))
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"ok"`)) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestActivateDatasetsConflictIsNonzeroAndSanitized(t *testing.T) {
	withDeploymentSecret(t)
	fake := &fakeMetadataDeploymentClient{
		listResponses: []*storagepb.ListDatasetsRsp{{
			RetInfo:    storageSuccess(),
			Datasets:   []*storagepb.Dataset{{SpaceId: "space", DatasetId: "dataset", Status: "disabled", Revision: 4}},
			PageResult: &storagepb.PageResult{HasMore: false},
		}},
		check: map[string]*storagepb.CheckDatasetActivationRsp{
			"space/dataset": {RetInfo: storageSuccess(), DatasetRevision: 4, Ready: true},
		},
		activate: map[string]*storagepb.ActivateDatasetRsp{
			"space/dataset": {RetInfo: storageFailure(storagepb.ErrorCode_CONFLICT)},
		},
	}
	var stdout bytes.Buffer
	if err := runActivateDatasets(context.Background(), []string{"--metadata-target", "ip://127.0.0.1:20100"}, &stdout, func(string) metadataDeploymentClient { return fake }); err == nil {
		t.Fatal("activation conflict must be nonzero")
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"failed"`)) || !bytes.Contains(stdout.Bytes(), []byte(`"status":"conflict"`)) {
		t.Fatalf("unexpected conflict output: %s", stdout.String())
	}
}
