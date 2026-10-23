package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetInstanceIncludingDeletedReturnsSoftDeletedIdentity(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertInstance(ctx, FunctionInstance{SpaceID: "crypto", InstanceID: "instance-deleted", Region: "ap-guangzhou"}))
	require.NoError(t, repo.DeleteInstances(ctx, "crypto", []string{"instance-deleted"}))

	current, err := repo.GetInstance(ctx, "crypto", "instance-deleted")
	require.NoError(t, err)
	require.Nil(t, current)
	deleted, err := repo.GetInstanceIncludingDeleted(ctx, "crypto", "instance-deleted")
	require.NoError(t, err)
	require.NotNil(t, deleted)
	require.True(t, deleted.IsDeleted)
}

func TestUpsertInstanceAssignsAndRotatesLifecycleIdentityOnlyOnRecreation(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	instance := FunctionInstance{SpaceID: "crypto", InstanceID: "reused", Region: "ap-singapore"}
	require.NoError(t, repo.UpsertInstance(ctx, instance))
	first, err := repo.GetInstance(ctx, "crypto", "reused")
	require.NoError(t, err)
	require.NotEmpty(t, first.LifecycleID)

	instance.Region = "ap-shanghai"
	require.NoError(t, repo.UpsertInstance(ctx, instance))
	updated, err := repo.GetInstance(ctx, "crypto", "reused")
	require.NoError(t, err)
	require.Equal(t, first.LifecycleID, updated.LifecycleID, "catalog updates must preserve the generation")

	require.NoError(t, repo.DeleteInstances(ctx, "crypto", []string{"reused"}))
	require.NoError(t, repo.UpsertInstance(ctx, instance))
	recreated, err := repo.GetInstance(ctx, "crypto", "reused")
	require.NoError(t, err)
	require.NotEmpty(t, recreated.LifecycleID)
	require.NotEqual(t, first.LifecycleID, recreated.LifecycleID, "reusing a instance_id must create a new generation")
}

func TestInstanceMutationClaimFencesDeploymentCommit(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertInstance(ctx, FunctionInstance{SpaceID: "crypto", InstanceID: "mutation-instance", PackageID: "old-package"}))
	instance, err := repo.GetInstance(ctx, "crypto", "mutation-instance")
	require.NoError(t, err)
	require.NotNil(t, instance)

	require.NoError(t, repo.ClaimInstanceMutation(ctx, "crypto", instance.InstanceID, instance.LifecycleID, "deploy-op"))
	require.ErrorIs(t, repo.ClaimInstanceMutation(ctx, "crypto", instance.InstanceID, instance.LifecycleID, "other-op"), ErrInstanceMutationClaimed)
	stillActive, err := repo.GetInstance(ctx, "crypto", instance.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, stillActive)
	require.Equal(t, "deploy-op", stillActive.LifecycleOperationID)

	require.NoError(t, repo.UpdateInstanceDeployment(ctx, "crypto", instance.InstanceID, "new-package", "v2", nil, nil, false, instance.LifecycleID, "deploy-op"))
	deployed, err := repo.GetInstance(ctx, "crypto", instance.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, deployed)
	require.Equal(t, "new-package", deployed.PackageID)
	require.Equal(t, "deploy-op", deployed.LifecycleOperationID, "the durable batch item owns claim release after catalog persistence")
	require.NoError(t, repo.ReleaseInstanceMutationClaim(ctx, "crypto", instance.InstanceID, instance.LifecycleID, "deploy-op"))
}

func TestInstanceMutationDeploymentCommitRejectsStaleGeneration(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertInstance(ctx, FunctionInstance{SpaceID: "crypto", InstanceID: "generation-instance", PackageID: "old-package"}))
	instance, err := repo.GetInstance(ctx, "crypto", "generation-instance")
	require.NoError(t, err)
	require.NotNil(t, instance)
	require.NoError(t, repo.ClaimInstanceMutation(ctx, "crypto", instance.InstanceID, instance.LifecycleID, "deploy-op"))

	// Simulate another process replacing the row generation between the remote
	// SCF call and its catalog commit. The stale worker must not write into it.
	require.NoError(t, repo.db.Model(&FunctionInstance{}).Where("c_id = ?", instance.ID).Update("c_lifecycle_id", "new-generation").Error)
	err = repo.UpdateInstanceDeployment(ctx, "crypto", instance.InstanceID, "new-package", "v2", nil, nil, false, instance.LifecycleID, "deploy-op")
	require.True(t, errors.Is(err, ErrInstanceLifecycleMismatch), "stale generation commit should fail closed: %v", err)
	current, err := repo.GetInstance(ctx, "crypto", instance.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, "old-package", current.PackageID)
	require.Equal(t, "new-generation", current.LifecycleID)
}

func TestReserveInstanceCreateRejectsCompetingReservationNonce(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	base := FunctionInstance{SpaceID: "crypto", InstanceID: "reserved-instance", PackageID: "pkg-a", Metadata: `{"deployment_ready":false}`}
	require.NoError(t, repo.ReserveInstanceCreate(ctx, base, "reservation-a"))

	conflicting := base
	conflicting.PackageID = "pkg-b"
	err := repo.ReserveInstanceCreate(ctx, conflicting, "reservation-b")
	require.ErrorIs(t, err, ErrInstanceLifecycleMismatch)
	instance, err := repo.GetInstance(ctx, "crypto", base.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, instance)
	require.Equal(t, "reservation-a", instance.LifecycleID)
	require.Equal(t, "pkg-a", instance.PackageID, "the conflicting request must not rewrite the winner's reservation")

	base.Metadata = `{"deployment_ready":false,"attempt":2}`
	require.NoError(t, repo.ReserveInstanceCreate(ctx, base, "reservation-a"), "a retry by the owning durable request may refresh its own reservation")
	instance, err = repo.GetInstance(ctx, "crypto", base.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, instance)
	require.Equal(t, "reservation-a", instance.LifecycleID)
}
