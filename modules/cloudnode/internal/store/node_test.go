package store

import (
	"context"
	"errors"
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

func TestGetNodeIncludingDeletedReturnsSoftDeletedIdentity(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertNode(ctx, CloudNode{SpaceID: "crypto", NodeID: "node-deleted", Region: "ap-guangzhou"}))
	require.NoError(t, repo.DeleteNodes(ctx, "crypto", []string{"node-deleted"}))

	current, err := repo.GetNode(ctx, "crypto", "node-deleted")
	require.NoError(t, err)
	require.Nil(t, current)
	deleted, err := repo.GetNodeIncludingDeleted(ctx, "crypto", "node-deleted")
	require.NoError(t, err)
	require.NotNil(t, deleted)
	require.True(t, deleted.IsDeleted)
}

func TestUpsertNodeAssignsAndRotatesLifecycleIdentityOnlyOnRecreation(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	node := CloudNode{SpaceID: "crypto", NodeID: "reused", Region: "ap-singapore"}
	require.NoError(t, repo.UpsertNode(ctx, node))
	first, err := repo.GetNode(ctx, "crypto", "reused")
	require.NoError(t, err)
	require.NotEmpty(t, first.LifecycleID)

	node.Region = "ap-shanghai"
	require.NoError(t, repo.UpsertNode(ctx, node))
	updated, err := repo.GetNode(ctx, "crypto", "reused")
	require.NoError(t, err)
	require.Equal(t, first.LifecycleID, updated.LifecycleID, "catalog updates must preserve the generation")

	require.NoError(t, repo.DeleteNodes(ctx, "crypto", []string{"reused"}))
	require.NoError(t, repo.UpsertNode(ctx, node))
	recreated, err := repo.GetNode(ctx, "crypto", "reused")
	require.NoError(t, err)
	require.NotEmpty(t, recreated.LifecycleID)
	require.NotEqual(t, first.LifecycleID, recreated.LifecycleID, "reusing a node_id must create a new generation")
}

func TestNodeMutationClaimFencesLegacyDeleteAndDeploymentCommit(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertNode(ctx, CloudNode{SpaceID: "crypto", NodeID: "mutation-node", PackageID: "old-package"}))
	node, err := repo.GetNode(ctx, "crypto", "mutation-node")
	require.NoError(t, err)
	require.NotNil(t, node)

	require.NoError(t, repo.ClaimNodeMutation(ctx, "crypto", node.NodeID, node.LifecycleID, "deploy-op"))
	require.ErrorIs(t, repo.ClaimNodeMutation(ctx, "crypto", node.NodeID, node.LifecycleID, "other-op"), ErrNodeMutationClaimed)
	require.Error(t, repo.DeleteNodesWithPreflight(ctx, "crypto", []string{node.NodeID}, nil), "catalog-only delete must not bypass an active deploy claim")
	stillActive, err := repo.GetNode(ctx, "crypto", node.NodeID)
	require.NoError(t, err)
	require.NotNil(t, stillActive)
	require.Equal(t, "deploy-op", stillActive.LifecycleOperationID)

	require.NoError(t, repo.UpdateNodeDeployment(ctx, "crypto", node.NodeID, "new-package", "v2", nil, nil, false, node.LifecycleID, "deploy-op"))
	deployed, err := repo.GetNode(ctx, "crypto", node.NodeID)
	require.NoError(t, err)
	require.NotNil(t, deployed)
	require.Equal(t, "new-package", deployed.PackageID)
	require.Equal(t, "deploy-op", deployed.LifecycleOperationID, "the durable batch item owns claim release after catalog persistence")
	require.Error(t, repo.DeleteNodesWithPreflight(ctx, "crypto", []string{node.NodeID}, nil), "the claim must survive until the durable operation is terminal")
	require.NoError(t, repo.ReleaseNodeMutationClaim(ctx, "crypto", node.NodeID, node.LifecycleID, "deploy-op"))
	require.NoError(t, repo.DeleteNodesWithPreflight(ctx, "crypto", []string{node.NodeID}, nil))
}

func TestNodeMutationDeploymentCommitRejectsStaleGeneration(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertNode(ctx, CloudNode{SpaceID: "crypto", NodeID: "generation-node", PackageID: "old-package"}))
	node, err := repo.GetNode(ctx, "crypto", "generation-node")
	require.NoError(t, err)
	require.NotNil(t, node)
	require.NoError(t, repo.ClaimNodeMutation(ctx, "crypto", node.NodeID, node.LifecycleID, "deploy-op"))

	// Simulate another process replacing the row generation between the remote
	// SCF call and its catalog commit. The stale worker must not write into it.
	require.NoError(t, repo.db.Model(&CloudNode{}).Where("c_id = ?", node.ID).Update("c_lifecycle_id", "new-generation").Error)
	err = repo.UpdateNodeDeployment(ctx, "crypto", node.NodeID, "new-package", "v2", nil, nil, false, node.LifecycleID, "deploy-op")
	require.True(t, errors.Is(err, ErrNodeLifecycleMismatch), "stale generation commit should fail closed: %v", err)
	current, err := repo.GetNode(ctx, "crypto", node.NodeID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, "old-package", current.PackageID)
	require.Equal(t, "new-generation", current.LifecycleID)
}

func TestReserveNodeCreateRejectsCompetingReservationNonce(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	base := CloudNode{SpaceID: "crypto", NodeID: "reserved-node", PackageID: "pkg-a", Metadata: `{"deployment_ready":false}`}
	require.NoError(t, repo.ReserveNodeCreate(ctx, base, "reservation-a"))

	conflicting := base
	conflicting.PackageID = "pkg-b"
	err := repo.ReserveNodeCreate(ctx, conflicting, "reservation-b")
	require.ErrorIs(t, err, ErrNodeLifecycleMismatch)
	node, err := repo.GetNode(ctx, "crypto", base.NodeID)
	require.NoError(t, err)
	require.NotNil(t, node)
	require.Equal(t, "reservation-a", node.LifecycleID)
	require.Equal(t, "pkg-a", node.PackageID, "the conflicting request must not rewrite the winner's reservation")

	base.Metadata = `{"deployment_ready":false,"attempt":2}`
	require.NoError(t, repo.ReserveNodeCreate(ctx, base, "reservation-a"), "a retry by the owning durable request may refresh its own reservation")
	node, err = repo.GetNode(ctx, "crypto", base.NodeID)
	require.NoError(t, err)
	require.NotNil(t, node)
	require.Equal(t, "reservation-a", node.LifecycleID)
}
