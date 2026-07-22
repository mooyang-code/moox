package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type activationMetadataReader struct {
	metadata.Store
	dataset *pb.Dataset
	node    *pb.DataNode
}

func (s *activationMetadataReader) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	if s.dataset == nil {
		return nil, errors.New("dataset not found")
	}
	return s.dataset, nil
}

func (s *activationMetadataReader) GetDataNode(context.Context, string) (*pb.DataNode, error) {
	if s.node == nil {
		return nil, errors.New("data node not found")
	}
	return s.node, nil
}

type fakeNodeStateChecker struct {
	rsp    *pb.GetNodeStateRsp
	err    error
	calls  int
	target string
	req    *pb.GetNodeStateReq
	wait   bool
}

func (f *fakeNodeStateChecker) GetNodeState(ctx context.Context, target string, req *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	f.calls++
	f.target = target
	f.req = req
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.rsp, f.err
}

func readyNodeState(nodeID string) *pb.GetNodeStateRsp {
	return &pb.GetNodeStateRsp{RetInfo: retinfo.Success("success"), NodeId: nodeID, Status: "READY"}
}

func newActivationReader(status, target string) *activationMetadataReader {
	return &activationMetadataReader{
		dataset: &pb.Dataset{
			SpaceId: "space-a", DatasetId: "dataset_a", DataSourceId: "source-a",
			Name: "数据集", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
			DataNodeId: "node-a", KeepDuration: "24h", Status: status, Revision: 7,
		},
		node: &pb.DataNode{NodeId: "node-a", ServiceTarget: target, Status: "active"},
	}
}

func TestDatasetActivationChecksAreOrderedAndSigned(t *testing.T) {
	reader := newActivationReader("disabled", "ip://127.0.0.1:19090")
	node := &fakeNodeStateChecker{rsp: readyNodeState("node-a")}
	checker := newActivationChecker(reader, node, "secret")

	checks := checker.checks(context.Background(), reader.dataset)
	require.Equal(t, activationCheckIDs, checkIDs(checks))
	require.True(t, activationReady(checks))
	require.Equal(t, 1, node.calls)
	require.Equal(t, "ip://127.0.0.1:19090", node.target)
	require.Equal(t, "node-a", node.req.GetNodeId())
	require.Equal(t, "storage-metadata", node.req.GetAuthInfo().GetAppId())
	require.Equal(t, datanode.ServiceAuthKey("secret", activationAppID), node.req.GetAuthInfo().GetAppKey(), "fake boundary sees the signed request")
}

func TestDatasetActivationCheckerRedactsRuntimeFailures(t *testing.T) {
	cases := []struct {
		name      string
		reader    *activationMetadataReader
		checker   *fakeNodeStateChecker
		wantCheck string
	}{
		{name: "malformed target", reader: newActivationReader("disabled", "http://127.0.0.1:1"), checker: &fakeNodeStateChecker{rsp: readyNodeState("node-a")}, wantCheck: "service_target"},
		{name: "disabled node", reader: func() *activationMetadataReader {
			r := newActivationReader("disabled", "ip://127.0.0.1:1")
			r.node.Status = "disabled"
			return r
		}(), checker: &fakeNodeStateChecker{rsp: readyNodeState("node-a")}, wantCheck: "data_node"},
		{name: "missing node", reader: func() *activationMetadataReader {
			r := newActivationReader("disabled", "ip://127.0.0.1:1")
			r.node = nil
			return r
		}(), checker: &fakeNodeStateChecker{rsp: readyNodeState("node-a")}, wantCheck: "data_node"},
		{name: "not ready", reader: newActivationReader("disabled", "ip://127.0.0.1:1"), checker: &fakeNodeStateChecker{rsp: &pb.GetNodeStateRsp{RetInfo: retinfo.Success("success"), NodeId: "node-a", Status: "STARTING"}}, wantCheck: "data_node_readiness"},
		{name: "signed error", reader: newActivationReader("disabled", "ip://127.0.0.1:1"), checker: &fakeNodeStateChecker{rsp: &pb.GetNodeStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("secret=do-not-leak"))}}, wantCheck: "data_node_readiness"},
		{name: "identity mismatch", reader: newActivationReader("disabled", "ip://127.0.0.1:1"), checker: &fakeNodeStateChecker{rsp: readyNodeState("node-b")}, wantCheck: "data_node_identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checks := newActivationChecker(tc.reader, tc.checker, "secret").checks(context.Background(), tc.reader.dataset)
			failed := checkByID(checks, tc.wantCheck)
			require.NotNil(t, failed)
			require.False(t, failed.GetReady())
			require.NotContains(t, failed.GetSummary(), "secret")
			if tc.name == "malformed target" || tc.name == "disabled node" || tc.name == "missing node" {
				require.Zero(t, tc.checker.calls)
			}
		})
	}
}

func TestDatasetActivationCheckerBoundsTimeout(t *testing.T) {
	reader := newActivationReader("disabled", "ip://127.0.0.1:1")
	fake := &fakeNodeStateChecker{wait: true}
	checker := newActivationChecker(reader, fake, "secret")
	checker.timeout = 10 * time.Millisecond

	start := time.Now()
	checks := checker.checks(context.Background(), reader.dataset)
	require.Less(t, time.Since(start), time.Second)
	require.False(t, checkByID(checks, "data_node_readiness").GetReady())
	require.Equal(t, activationCheckIDs, checkIDs(checks))
}

func checkIDs(checks []*pb.DatasetActivationCheck) []string {
	ids := make([]string, 0, len(checks))
	for _, check := range checks {
		ids = append(ids, check.GetCheckId())
	}
	return ids
}

func checkByID(checks []*pb.DatasetActivationCheck, id string) *pb.DatasetActivationCheck {
	for _, check := range checks {
		if check.GetCheckId() == id {
			return check
		}
	}
	return nil
}
