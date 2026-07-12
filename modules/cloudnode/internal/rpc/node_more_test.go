package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestNodeMetadataAndStatusBranches(t *testing.T) {
	meta, err := structpb.NewStruct(map[string]any{"existing": "yes"})
	require.NoError(t, err)
	node := &pb.CloudNode{
		Metadata:           meta,
		BizType:            "collect.kline",
		Tag:                "prod",
		IpAddress:          "10.0.0.1",
		TimeoutThreshold:   30,
		HeartbeatInterval:  15,
		ProbeEnabled:       true,
		ProbeUrl:           "https://probe",
		ClsTopicId:         "topic-1",
		SupportedWorkloads: []string{"collect.kline"},
	}
	got := nodeMetadataFromPB(node)
	assert.Equal(t, "yes", got["existing"])
	assert.Equal(t, "collect.kline", got["biz_type"])
	assert.Equal(t, true, got["probe_enabled"])
	assert.Equal(t, "topic-1", got["cls_topic_id"])
	assert.Empty(t, nodeMetadataFromPB(nil))

	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_TIMEOUT, nodeStatusToPB(" timeout "))
	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_ABNORMAL, nodeStatusToPB("abnormal"))
	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_OFFLINE, nodeStatusToPB("unknown"))
	assert.Equal(t, pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED, nodeStatusToPB("starting"))

	assert.Equal(t, "timeout", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_TIMEOUT))
	assert.Equal(t, "abnormal", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_ABNORMAL))
	assert.Equal(t, "offline", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_OFFLINE))
	assert.Equal(t, "unknown", nodeStatusToDB(pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED))
}

func TestNodeConversionHelpersCoverDefaults(t *testing.T) {
	assert.Nil(t, parseStringSliceJSON(""))
	assert.Nil(t, parseStringSliceJSON("[]"))
	assert.Nil(t, parseStringSliceJSON("{bad"))

	copied := copyStringMap(map[string]string{"a": "1"})
	copied["a"] = "2"
	assert.Equal(t, "1", map[string]string{"a": "1"}["a"])
	assert.Nil(t, copyStringMap(nil))

	meta, err := structpb.NewStruct(map[string]any{
		"function_name":       "fn-meta",
		"supported_workloads": []any{"collect.symbol"},
	})
	require.NoError(t, err)
	node := fromPBNode("space", &pb.CloudNode{
		NodeId:   "node-1",
		Metadata: meta,
		Status:   pb.NodeStatusCode_NODE_STATUS_ABNORMAL,
	})
	assert.Equal(t, "scf-event", node.NodeType)
	assert.Equal(t, "tencent-scf", node.Provider)
	assert.Equal(t, "fn-meta", node.FunctionName)
	assert.Contains(t, node.SupportedWorkloads, "collect.symbol")
	assert.Equal(t, "abnormal", node.Status)
}

func TestMergeNodeUpdateCoversOptionalFields(t *testing.T) {
	meta, err := structpb.NewStruct(map[string]any{"new": "value"})
	require.NoError(t, err)
	existing := store.CloudNode{
		SpaceID: "space", NodeID: "node-1", CloudAccountID: "old-acct",
		Metadata: `{"old":"value"}`, Status: "online",
	}
	next := mergeNodeUpdate(existing, &pb.CloudNode{
		CloudAccountId:     "new-acct",
		PackageId:          "pkg-1",
		PackageVersion:     "v2",
		DeploymentId:       "dep-1",
		NodeType:           "worker",
		Provider:           "tencent-scf",
		Region:             "ap-guangzhou",
		Namespace:          "default",
		FunctionName:       "fn",
		RunningVersion:     "rv1",
		SupportedWorkloads: []string{"collect.kline"},
		Metadata:           meta,
		Status:             pb.NodeStatusCode_NODE_STATUS_OFFLINE,
		IsDeleted:          true,
	})
	assert.Equal(t, "new-acct", next.CloudAccountID)
	assert.Equal(t, "pkg-1", next.PackageID)
	assert.Equal(t, "v2", next.PackageVersion)
	assert.Equal(t, "dep-1", next.DeploymentID)
	assert.Equal(t, "worker", next.NodeType)
	assert.Equal(t, "tencent-scf", next.Provider)
	assert.Equal(t, "ap-guangzhou", next.Region)
	assert.Equal(t, "default", next.Namespace)
	assert.Equal(t, "fn", next.FunctionName)
	assert.Equal(t, "rv1", next.RunningVersion)
	assert.Contains(t, next.SupportedWorkloads, "collect.kline")
	assert.Contains(t, next.Metadata, `"old":"value"`)
	assert.Contains(t, next.Metadata, `"new":"value"`)
	assert.Equal(t, "offline", next.Status)
	assert.True(t, next.IsDeleted)
}
