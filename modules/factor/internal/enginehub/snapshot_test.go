package enginehub

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestSnapshotOnlyEnabledSetsAndMembersWithSource(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedSet(t, db, "set_a", map[string]string{"bias": domain.MemberStatusEnabled, "cci": domain.MemberStatusDisabled})
	disabled := seedSet(t, db, "set_b", map[string]string{"bias": domain.MemberStatusEnabled})
	require.NoError(t, db.SetSetStatus(context.Background(), disabled.SetID, domain.SetStatusEnabled, domain.SetStatusDisabled))

	hash, notModified, sets, err := hub.Snapshot(context.Background(), "")

	require.NoError(t, err)
	require.False(t, notModified)
	require.NotEmpty(t, hash)
	require.Len(t, sets, 1)
	require.Equal(t, "set_a", sets[0].Set.SetID)
	require.Len(t, sets[0].Factors, 1)
	require.Equal(t, "bias", sets[0].Factors[0].FactorID)
	require.NotEmpty(t, sets[0].Factors[0].SourceCode)
	require.False(t, sets[0].ResultReady)
}

func TestSnapshotNotModifiedWhenHashMatches(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedSet(t, db, "set_a", map[string]string{"bias": domain.MemberStatusEnabled})
	hash, _, _, err := hub.Snapshot(context.Background(), "")
	require.NoError(t, err)

	again, notModified, sets, err := hub.Snapshot(context.Background(), hash)

	require.NoError(t, err)
	require.True(t, notModified)
	require.Nil(t, sets)
	require.Equal(t, hash, again)
}

func TestCatalogHashChanges(t *testing.T) {
	hub, db, _ := newTestHub(t)
	ctx := context.Background()
	seedSet(t, db, "set_a", map[string]string{"bias": domain.MemberStatusEnabled, "cci": domain.MemberStatusDisabled})
	base, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)

	hub.SetResultReady("set_a", true)
	ready, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.NotEqual(t, base, ready, "result readiness changes the hash")

	require.NoError(t, db.SetMemberStatus(ctx, "set_a", "cci", domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	member, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.NotEqual(t, ready, member, "enabling a member changes the hash")

	require.NoError(t, db.SetMemberStatus(ctx, "set_a", "cci", domain.MemberStatusEnabled, domain.MemberStatusDisabled))
	edited := testFactor("cci")
	edited.SourceCode += "\n# edited"
	edited.SourceHash = domain.SourceHash(edited.SourceCode)
	require.NoError(t, db.UpdateFactor(ctx, edited))
	unchanged, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.Equal(t, ready, unchanged, "editing a disabled member does not change what the engine computes")

	set, err := db.GetSet(ctx, "set_a")
	require.NoError(t, err)
	set.SubjectMode, set.Subjects = domain.SubjectModeInclude, []string{"BTC"}
	require.NoError(t, db.UpdateSet(ctx, set))
	scoped, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.NotEqual(t, ready, scoped, "changing the set scope changes the hash")
}

func TestCatalogHashIgnoresTimestamps(t *testing.T) {
	sets := []domain.EngineSet{{Set: domain.FactorSet{SetID: "set_a"}, Factors: []domain.FactorDef{testFactor("bias")}}}
	first, err := CatalogHash(sets)
	require.NoError(t, err)
	sets[0].Set.UpdatedAt = sets[0].Set.UpdatedAt.Add(1)
	sets[0].Factors[0].UpdatedAt = sets[0].Factors[0].UpdatedAt.Add(1)

	second, err := CatalogHash(sets)

	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestCatalogInSyncComparesHeartbeatHash(t *testing.T) {
	hub, db, _ := newTestHub(t)
	ctx := context.Background()
	seedSet(t, db, "set_a", map[string]string{"bias": domain.MemberStatusEnabled})
	hash, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)

	_, err = hub.Heartbeat(ctx, engineA, domain.EngineStatus{CatalogHash: hash})
	require.NoError(t, err)
	info, _, _, err := hub.Engine(ctx)
	require.NoError(t, err)
	require.True(t, info.CatalogInSync)

	hub.SetResultReady("set_a", true)
	info, _, _, err = hub.Engine(ctx)
	require.NoError(t, err)
	require.False(t, info.CatalogInSync)
}
