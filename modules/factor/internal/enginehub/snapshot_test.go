package enginehub

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSnapshotOnlyEnabledTasksAndMembersWithSource(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled, "cci": domain.MemberStatusDisabled})
	disabled := seedTask(t, db, "task_b", map[string]string{"bias": domain.MemberStatusEnabled})
	require.NoError(t, db.SetTaskStatus(context.Background(), disabled.TaskID, domain.TaskStatusEnabled, domain.TaskStatusDisabled))

	hash, notModified, sets, err := hub.Snapshot(context.Background(), "")

	require.NoError(t, err)
	require.False(t, notModified)
	require.NotEmpty(t, hash)
	require.Len(t, sets, 1)
	require.Equal(t, "task_a", sets[0].Set.TaskID)
	require.Len(t, sets[0].Members, 1)
	require.Equal(t, "bias", sets[0].Members[0].Factor.FactorID)
	require.NotEmpty(t, sets[0].Members[0].Factor.Spec)
	require.Equal(t, []string{"bias"}, sets[0].Members[0].Factor.Outputs)
	require.False(t, sets[0].ResultReady)
}

func TestSnapshotNotModifiedWhenHashMatches(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
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
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled, "cci": domain.MemberStatusDisabled})
	base, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)

	hub.SetResultReady("task_a", true)
	ready, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.NotEqual(t, base, ready, "result readiness changes the hash")

	require.NoError(t, db.SetMemberStatus(ctx, "task_a", "cci", domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	member, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.NotEqual(t, ready, member, "enabling a member changes the hash")

	require.NoError(t, db.SetMemberStatus(ctx, "task_a", "cci", domain.MemberStatusEnabled, domain.MemberStatusDisabled))
	edited := domain.MustDefinition("cci", "cci", "output: close * 2\n")
	require.NoError(t, db.UpdateFactor(ctx, edited))
	unchanged, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.Equal(t, ready, unchanged, "editing a disabled member does not change what the engine computes")

}

// 成员的固定参数进目录指纹：同一成员键换了参数，引擎必须重新同步。
func TestCatalogHashCoversMemberParams(t *testing.T) {
	member := domain.AsMembers(testFactor("bias"))[0]
	base := []domain.EngineTask{{Set: domain.FactorTask{TaskID: "task_a"}, Members: []domain.MemberDef{member}}}
	first, err := CatalogHash(base)
	require.NoError(t, err)

	member.Params = map[string]float64{"window": 20}
	withParams, err := CatalogHash([]domain.EngineTask{{Set: domain.FactorTask{TaskID: "task_a"}, Members: []domain.MemberDef{member}}})
	require.NoError(t, err)
	require.NotEqual(t, first, withParams)

	member.Params = map[string]float64{"window": 50}
	other, err := CatalogHash([]domain.EngineTask{{Set: domain.FactorTask{TaskID: "task_a"}, Members: []domain.MemberDef{member}}})
	require.NoError(t, err)
	require.NotEqual(t, withParams, other)
}

func TestCatalogHashIgnoresTimestamps(t *testing.T) {
	sets := []domain.EngineTask{{Set: domain.FactorTask{TaskID: "task_a"}, Members: domain.AsMembers(testFactor("bias"))}}
	first, err := CatalogHash(sets)
	require.NoError(t, err)
	sets[0].Set.UpdatedAt = sets[0].Set.UpdatedAt.Add(1)
	sets[0].Members[0].Factor.UpdatedAt = sets[0].Members[0].Factor.UpdatedAt.Add(1)

	second, err := CatalogHash(sets)

	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestCatalogInSyncComparesHeartbeatHash(t *testing.T) {
	hub, db, _ := newTestHub(t)
	ctx := context.Background()
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
	hash, _, _, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)

	_, err = hub.Heartbeat(ctx, engineA, domain.EngineStatus{CatalogHash: hash})
	require.NoError(t, err)
	info, _, _, err := hub.Engine(ctx)
	require.NoError(t, err)
	require.True(t, info.CatalogInSync)

	hub.SetResultReady("task_a", true)
	info, _, _, err = hub.Engine(ctx)
	require.NoError(t, err)
	require.False(t, info.CatalogInSync)
}

// 源数据集在历史补齐中：任务的 SourceSeeding 为真并进目录指纹；补齐结束后恢复，指纹随之变化。
func TestSnapshotMarksSourceSeedingAndHashesIt(t *testing.T) {
	hub, db, _ := newTestHub(t)
	ctx := context.Background()
	task := seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
	base, _, sets, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.False(t, sets[0].SourceSeeding)

	hub.SetHistorySeeds([]store.SeedKey{
		{SpaceID: task.SpaceID, DatasetID: task.SourceDatasetID, Freq: task.Freq},
		{SpaceID: "other", DatasetID: "other", Freq: "1m"},
	})
	seeding, notModified, sets, err := hub.Snapshot(ctx, base)
	require.NoError(t, err)
	require.False(t, notModified)
	require.True(t, sets[0].SourceSeeding)
	require.NotEqual(t, base, seeding, "补齐状态进目录指纹")

	// 别的数据集在补齐不影响本任务。
	hub.SetHistorySeeds([]store.SeedKey{{SpaceID: task.SpaceID, DatasetID: "unrelated", Freq: task.Freq}})
	released, _, sets, err := hub.Snapshot(ctx, "")
	require.NoError(t, err)
	require.False(t, sets[0].SourceSeeding)
	require.Equal(t, base, released)
}
