package store

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/require"
)

func TestNodeInvocationSelection(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	nodes := []CloudNode{
		{SpaceID: "crypto", NodeID: "offline", NodeType: "scf-event", DeploymentID: "offline"},
		{SpaceID: "crypto", NodeID: "online", NodeType: "scf-event", DeploymentID: "online"},
	}
	for _, node := range nodes {
		require.NoError(t, repo.UpsertNode(ctx, node))
	}

	selected, err := repo.FindNodeForInvocation(ctx, "crypto", "online", "collect.kline")
	require.NoError(t, err)
	require.NotNil(t, selected)

	online, total, err := repo.ListNodes(ctx, "crypto", &pb.GetNodeListReq{Page: &pb.Page{Page: 1, Size: 20}})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, online, 2)
}
