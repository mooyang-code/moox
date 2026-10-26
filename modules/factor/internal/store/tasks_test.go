package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.ApplySchema(factorschema.AllSQL()))
	return s
}

func testTask(taskID, status string) domain.FactorTask {
	return domain.FactorTask{
		TaskID: taskID, SpaceID: "crypto", Name: taskID, SourceDatasetID: "dataset_prices", Freq: "1m",
		ResultDatasetID: "dataset_factor_prices", Status: status,
	}
}

func testFactorDef(factorID string) domain.FactorDef {
	return domain.MustDefinition(factorID, factorID, "output: close\n")
}

// changedSpec 返回改了表达式的同一个定义（输出列不变）。
func changedSpec(def domain.FactorDef, spec string) domain.FactorDef {
	changed := domain.MustDefinition(def.FactorID, def.Name, spec)
	changed.CreatedAt = def.CreatedAt
	return changed
}

// addTestMember creates the definition if needed and attaches it to the set with the given status.
func addTestMember(t *testing.T, s *Store, taskID, factorID, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetFactor(ctx, factorID); err != nil {
		require.NoError(t, s.CreateFactor(ctx, testFactorDef(factorID)))
	}
	_, err := s.AddMember(ctx, taskID, factorID, factorID, domain.MemberKindFixed, nil)
	require.NoError(t, err)
	if status == domain.MemberStatusEnabled {
		require.NoError(t, s.SetMemberStatus(ctx, taskID, factorID, domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	}
}

func TestCreateFactorsRollsBackCatalogImportOnConflict(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))
	factor := testFactorDef("factor_close")
	err := s.CreateFactors(ctx, []domain.FactorDef{factor, factor})
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.GetFactor(ctx, factor.FactorID)
	require.Error(t, err)
}

// 同一个源数据集与频率可以有多个任务；任务 ID、结果数据集与空间内的名称仍各自唯一。
func TestCreateTaskAllowsSharedSourceButUniqueResultAndName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusEnabled)))

	second := testTask("task_prices_2", domain.TaskStatusPending)
	second.ResultDatasetID = "dataset_factor_second"
	require.NoError(t, s.CreateTask(ctx, second))

	duplicateResult := testTask("task_prices_3", domain.TaskStatusPending)
	require.ErrorIs(t, s.CreateTask(ctx, duplicateResult), ErrConflict)

	duplicateName := testTask("task_prices_4", domain.TaskStatusPending)
	duplicateName.ResultDatasetID, duplicateName.Name = "dataset_factor_fourth", "task_prices"
	require.ErrorIs(t, s.CreateTask(ctx, duplicateName), ErrConflict)

	otherSpace := duplicateName
	otherSpace.SpaceID = "stock"
	require.NoError(t, s.CreateTask(ctx, otherSpace), "名称只在空间内唯一")
}

// 关联实例独占且永久：同一个实例重复关联幂等；任务不能换实例；一个实例只能关联一个任务；已关联的任务不能删除。
func TestBindTaskIsExclusiveAndPermanent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_a", domain.TaskStatusEnabled)))
	other := testTask("task_b", domain.TaskStatusEnabled)
	other.ResultDatasetID = "dataset_factor_b"
	require.NoError(t, s.CreateTask(ctx, other))

	bound, err := s.BindTask(ctx, "task_a", "inst-1")
	require.NoError(t, err)
	require.Equal(t, "inst-1", bound.BoundInstanceID)
	again, err := s.BindTask(ctx, "task_a", "inst-1")
	require.NoError(t, err)
	require.Equal(t, bound.UpdatedAt, again.UpdatedAt)

	_, err = s.BindTask(ctx, "task_a", "inst-2")
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.BindTask(ctx, "task_b", "inst-1")
	require.ErrorIs(t, err, ErrConflict)
	_, err = s.BindTask(ctx, "missing", "inst-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	byInstance, err := s.GetTaskByInstance(ctx, "inst-1")
	require.NoError(t, err)
	require.Equal(t, "task_a", byInstance.TaskID)
	_, err = s.GetTaskByInstance(ctx, "inst-9")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	require.ErrorIs(t, s.DeleteTask(ctx, "task_a"), ErrConflict)
	require.NoError(t, s.DeleteTask(ctx, "task_b"))
}

func TestDeleteTaskFailsWhenMembersExist(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateTask(ctx, testTask("task_prices", domain.TaskStatusDisabled)))
	addTestMember(t, s, "task_prices", "factor_close", domain.MemberStatusDisabled)

	require.ErrorIs(t, s.DeleteTask(ctx, "task_prices"), ErrConflict)
	_, err := s.GetTask(ctx, "task_prices")
	require.NoError(t, err)
}

func TestTaskCRUDAndFactorCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	set := testTask("task_prices", domain.TaskStatusPending)
	require.NoError(t, s.CreateTask(ctx, set))
	got, err := s.GetTask(ctx, set.TaskID)
	require.NoError(t, err)
	require.Equal(t, set.SourceDatasetID, got.SourceDatasetID)
	require.Equal(t, domain.TaskStatusPending, got.Status)

	sets, err := s.ListTasks(ctx)
	require.NoError(t, err)
	require.Len(t, sets, 1)

	def := testFactorDef("factor_close")
	require.NoError(t, s.CreateFactor(ctx, def))
	def, err = s.GetFactor(ctx, def.FactorID)
	require.NoError(t, err)
	require.NotZero(t, def.CreatedAt)
	require.NotZero(t, def.UpdatedAt)
	def = changedSpec(def, "output: ts_mean(close, 10)\n")
	require.NoError(t, s.UpdateFactor(ctx, def))
	def, err = s.GetFactor(ctx, def.FactorID)
	require.NoError(t, err)
	require.Equal(t, 10, def.LookbackPeriods, "读取时编译得到派生字段")
	require.Equal(t, domain.SpecHash(def.Spec), def.SpecHash)

	defs, err := s.ListFactors(ctx)
	require.NoError(t, err)
	require.Len(t, defs, 1)
	require.NoError(t, s.DeleteFactor(ctx, def.FactorID))
	require.NoError(t, s.DeleteTask(ctx, set.TaskID))
}

func TestLifecycleStatusWritesUseExpectedStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	set := testTask("task_prices", domain.TaskStatusPending)
	require.NoError(t, s.CreateTask(ctx, set))
	require.NoError(t, s.SetTaskStatus(ctx, set.TaskID, domain.TaskStatusPending, domain.TaskStatusEnabled))
	require.ErrorIs(t, s.SetTaskStatus(ctx, set.TaskID, domain.TaskStatusPending, domain.TaskStatusDisabled), ErrConflict)
	storedTask, err := s.GetTask(ctx, set.TaskID)
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusEnabled, storedTask.Status)

	factor := testFactorDef("factor_close")
	require.NoError(t, s.CreateFactor(ctx, factor))
	_, err = s.AddMember(ctx, set.TaskID, factor.FactorID, factor.FactorID, domain.MemberKindFixed, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetMemberStatus(ctx, set.TaskID, factor.FactorID, domain.MemberStatusDisabled, domain.MemberStatusEnabled))
	require.ErrorIs(t, s.SetMemberStatus(ctx, set.TaskID, factor.FactorID, domain.MemberStatusDisabled, domain.MemberStatusDisabled), ErrConflict)
	member, err := s.GetMember(ctx, set.TaskID, factor.FactorID)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, member.Status)
}
