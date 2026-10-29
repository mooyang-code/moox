package command

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type fakeDataNodeRegistry struct {
	nodes      []string
	registered []*storagepb.RegisterDataNodeReq
}

func (f *fakeDataNodeRegistry) ListDataNodes(context.Context, *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error) {
	rsp := &storagepb.ListDataNodesRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}
	for _, id := range f.nodes {
		rsp.Items = append(rsp.Items, &storagepb.DataNodeListItem{Node: &storagepb.DataNode{NodeId: id}})
	}
	return rsp, nil
}

func (f *fakeDataNodeRegistry) RegisterDataNode(_ context.Context, req *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error) {
	f.registered = append(f.registered, req)
	f.nodes = append(f.nodes, req.GetNodeId())
	return &storagepb.RegisterDataNodeRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func TestEnsureNativeDataNodeRegistersOnce(t *testing.T) {
	api := &fakeDataNodeRegistry{}
	nodeAuth := &storagepb.AuthInfo{AppId: "storage-deployer"}
	for range 2 {
		if err := ensureNativeDataNode(context.Background(), api, nil, nodeAuth); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.registered) != 1 {
		t.Fatalf("应只登记一次，实际 %d 次", len(api.registered))
	}
	req := api.registered[0]
	if req.GetNodeId() != nativeDataNodeID || req.GetServiceTarget() != nativeDataNodeTarget || req.GetAuthInfo().GetAppId() != "storage-deployer" {
		t.Fatalf("登记请求 = %+v", req)
	}
}
