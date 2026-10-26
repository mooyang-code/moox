package store

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMemberUniqueAndForeignKeys(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	require.NoError(t, s.CreateFactor(ctx, testFactorDef("factor_close")))

	member, err := s.AddMember(ctx, "task_prices", "factor_close", "factor_close", nil)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, member.Status)
	require.NotZero(t, member.CreatedAt)

	_, err = s.AddMember(ctx, "task_prices", "factor_close", "factor_close", nil)
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.AddMember(ctx, "task_prices", "missing", "missing", nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = s.AddMember(ctx, "missing", "factor_close", "factor_close", nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSameDefinitionInTwoTasksHasIndependentStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_1m", domain.TaskStatusEnabled)))
	second := testTask("task_1h", domain.TaskStatusEnabled)
	second.Freq, second.ResultDatasetID = "1h", "dataset_factor_prices_1h"
	require.NoError(t, s.CreateTask(ctx, second))
	addTestMember(t, s, "task_1m", "bias", domain.MemberStatusEnabled)
	addTestMember(t, s, "task_1h", "bias", domain.MemberStatusDisabled)

	first, err := s.GetMember(ctx, "task_1m", "bias")
	require.NoError(t, err)
	other, err := s.GetMember(ctx, "task_1h", "bias")
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, first.Status)
	require.Equal(t, domain.MemberStatusDisabled, other.Status)
}

func TestRemoveMemberRequiresDisabled(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	addTestMember(t, s, "task_prices", "factor_close", domain.MemberStatusEnabled)

	require.ErrorIs(t, s.RemoveMember(ctx, "task_prices", "factor_close"), ErrConflict)
	require.NoError(t, s.SetMemberStatus(ctx, "task_prices", "factor_close", domain.MemberStatusEnabled, domain.MemberStatusDisabled))
	require.NoError(t, s.RemoveMember(ctx, "task_prices", "factor_close"))
	require.ErrorIs(t, s.RemoveMember(ctx, "task_prices", "factor_close"), gorm.ErrRecordNotFound)
	_, err := s.GetFactor(ctx, "factor_close")
	require.NoError(t, err, "removing a member keeps the definition")
}

func TestDeleteFactorRejectedWhileReferenced(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	addTestMember(t, s, "task_prices", "factor_close", domain.MemberStatusDisabled)

	require.ErrorIs(t, s.DeleteFactor(ctx, "factor_close"), ErrConflict)
	require.NoError(t, s.RemoveMember(ctx, "task_prices", "factor_close"))
	require.NoError(t, s.DeleteFactor(ctx, "factor_close"))
	require.ErrorIs(t, s.DeleteFactor(ctx, "factor_close"), gorm.ErrRecordNotFound)
}

func TestUpdateFactorRejectedWhileEnabledMemberExists(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	addTestMember(t, s, "task_prices", "factor_close", domain.MemberStatusEnabled)

	changed := changedSpec(testFactorDef("factor_close"), "outputs:\n  factor_close: close * 2\n")
	require.ErrorIs(t, s.UpdateFactor(ctx, changed), ErrConflict)
	stored, err := s.GetFactor(ctx, "factor_close")
	require.NoError(t, err)
	require.NotEqual(t, changed.Spec, stored.Spec)

	missing := testFactorDef("missing")
	require.ErrorIs(t, s.UpdateFactor(ctx, missing), gorm.ErrRecordNotFound)
}

func TestUpdateFactorAllowedWhenOnlyDisabledMembers(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	addTestMember(t, s, "task_prices", "factor_close", domain.MemberStatusDisabled)

	changed := changedSpec(testFactorDef("factor_close"), "outputs:\n  factor_close: close * 2\n")
	require.NoError(t, s.UpdateFactor(ctx, changed))
	stored, err := s.GetFactor(ctx, "factor_close")
	require.NoError(t, err)
	require.Equal(t, changed.Spec, stored.Spec)
	require.Equal(t, changed.SpecHash, stored.SpecHash)
}

func TestListUsagesAggregatesAcrossTasks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_1m", domain.TaskStatusEnabled)))
	second := testTask("task_1h", domain.TaskStatusEnabled)
	second.Freq, second.ResultDatasetID = "1h", "dataset_factor_prices_1h"
	require.NoError(t, s.CreateTask(ctx, second))
	addTestMember(t, s, "task_1m", "bias", domain.MemberStatusEnabled)
	addTestMember(t, s, "task_1h", "bias", domain.MemberStatusDisabled)
	require.NoError(t, s.CreateFactor(ctx, testFactorDef("unused")))

	usages, err := s.ListUsages(ctx, "bias", "unused")
	require.NoError(t, err)
	require.Equal(t, []domain.FactorUsage{
		{TaskID: "task_1h", MemberAlias: "bias", Status: domain.MemberStatusDisabled},
		{TaskID: "task_1m", MemberAlias: "bias", Status: domain.MemberStatusEnabled},
	}, usages["bias"])
	require.Empty(t, usages["unused"])

	all, err := s.ListUsages(ctx)
	require.NoError(t, err)
	require.Len(t, all["bias"], 2)

	count, err := s.CountMembers(ctx, "task_1m")
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestListMembersJoinsDefinitionsSortedByFactorID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	addTestMember(t, s, "task_prices", "z_factor", domain.MemberStatusDisabled)
	addTestMember(t, s, "task_prices", "a_factor", domain.MemberStatusEnabled)

	members, err := s.ListMembers(ctx, "task_prices", "")
	require.NoError(t, err)
	require.Len(t, members, 2)
	require.Equal(t, "a_factor", members[0].FactorID)
	require.Equal(t, "task_prices", members[0].TaskID)
	require.Equal(t, domain.MemberStatusEnabled, members[0].Status)
	require.Equal(t, "a_factor", members[0].Factor.FactorID)
	require.Equal(t, []string{"close"}, members[0].Factor.InputColumns)
	require.NotZero(t, members[0].Factor.CreatedAt)
	require.NotZero(t, members[0].CreatedAt)

	enabled, err := s.ListMembers(ctx, "task_prices", domain.MemberStatusEnabled)
	require.NoError(t, err)
	require.Len(t, enabled, 1)
}
