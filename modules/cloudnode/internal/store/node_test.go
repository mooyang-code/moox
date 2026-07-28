package store

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/require"
)

func TestSCFHeartbeatFreshnessControlsStatusAndScheduling(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	repo := newTestCatalog(t)
	repo.now = func() time.Time { return now }
	ctx := context.Background()

	nodes := []CloudNode{
		{SpaceID: "crypto", NodeID: "unknown", NodeType: "scf-event", DeploymentID: "unknown", SupportedWorkloads: `["collect.kline"]`},
		{SpaceID: "crypto", NodeID: "fresh", NodeType: "scf-event", DeploymentID: "fresh", SupportedWorkloads: `["collect.kline"]`, LastHeartbeatAt: timePtr(now.Add(-89 * time.Second))},
		{SpaceID: "crypto", NodeID: "stale", NodeType: "scf-event", DeploymentID: "stale", SupportedWorkloads: `["collect.kline"]`, LastHeartbeatAt: timePtr(now.Add(-91 * time.Second))},
	}
	for _, node := range nodes {
		require.NoError(t, repo.UpsertNode(ctx, node))
	}

	unknown, err := repo.GetNode(ctx, "crypto", "unknown")
	require.NoError(t, err)
	require.Equal(t, "unknown", unknown.Status)
	fresh, err := repo.GetNode(ctx, "crypto", "fresh")
	require.NoError(t, err)
	require.Equal(t, "online", fresh.Status)
	stale, err := repo.GetNode(ctx, "crypto", "stale")
	require.NoError(t, err)
	require.Equal(t, "timeout", stale.Status)

	selected, err := repo.FindNodeForInvocation(ctx, "crypto", "fresh", "collect.kline")
	require.NoError(t, err)
	require.NotNil(t, selected)
	for _, deploymentID := range []string{"unknown", "stale"} {
		selected, err = repo.FindNodeForInvocation(ctx, "crypto", deploymentID, "collect.kline")
		require.NoError(t, err)
		require.Nil(t, selected)
	}

	require.NoError(t, repo.UpdateHeartbeat(ctx, "crypto", "stale", "v1", `["collect.kline"]`, `{}`))
	stale, err = repo.GetNode(ctx, "crypto", "stale")
	require.NoError(t, err)
	require.Equal(t, "online", stale.Status)

	online, total, err := repo.ListNodes(ctx, "crypto", &pb.GetNodeListReq{
		Status: pb.NodeStatusCode_NODE_STATUS_ONLINE,
		Page:   &pb.Page{Page: 1, Size: 20},
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, online, 2)
}

func timePtr(value time.Time) *time.Time {
	return &value
}
