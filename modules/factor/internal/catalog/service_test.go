package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreateSetCreatesAndActivatesResultDataset(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)

	set, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	require.Equal(t, testTaskID(), set.TaskID)
	require.Equal(t, "dataset_factor_prices_1m", set.ResultDatasetID)
	require.Equal(t, domain.TaskStatusEnabled, set.Status)
	require.Equal(t, "storage-node-0", meta.createdSpec.DataNodeID)
	require.Equal(t, storageio.DatasetRoleFactorResult, meta.createdSpec.Attributes["dataset_role"])
	require.Equal(t, "factor", meta.createdSpec.Attributes["owner_module"])
	require.NotContains(t, meta.createdSpec.Attributes, "write_owner")
	require.Equal(t, []storageio.ColumnInfo{sourceColumn("close"), paramsColumn()}, meta.createdSpec.Columns, "结果数据集创建时同时注册 __params 列")
	require.Equal(t, "价格因子", meta.createdSpec.Name, "任务名称直接用作结果数据集的名称")
	require.Equal(t, storageio.DatasetStatusActive, meta.datasets[resultKey("crypto", set.ResultDatasetID)].Status)
	require.Equal(t, []string{"create", "activate"}, meta.writeOps)
}

func TestCreateTaskCanonicalizesUppercaseFrequency(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	source := meta.datasets[resultKey("crypto", "dataset_prices")]
	source.Freq = "1h"
	meta.datasets[resultKey("crypto", "dataset_prices")] = source
	set := newTask()
	set.Freq = "1H"

	created, err := NewService(db, meta).CreateTask(context.Background(), set)
	require.NoError(t, err)
	require.Equal(t, "1h", created.Freq)
	require.Equal(t, testTaskID(), created.TaskID)
	require.Equal(t, "dataset_factor_prices_1m", created.ResultDatasetID)
}

func TestCreateTaskRejectsSpaceWithoutCalendar(t *testing.T) {
	svc := NewService(openCatalogStore(t), newMetadataFake())
	set := newTask()
	set.SpaceID = "stockcn"

	_, err := svc.CreateTask(context.Background(), set)

	require.ErrorIs(t, err, periodclock.ErrUnsupportedCalendar)
}

func TestCreateTaskRetryResumesFromPending(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	meta.activateErrs = 1
	svc := NewService(db, meta)

	_, err := svc.CreateTask(context.Background(), newTask())
	require.ErrorContains(t, err, "activate")
	set, err := db.GetTask(context.Background(), testTaskID())
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusPending, set.Status)

	set, err = svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusEnabled, set.Status)
	require.Equal(t, 1, meta.createdDatasets)
	require.Equal(t, 2, meta.createCalls)
	require.Equal(t, storageio.DatasetStatusActive, meta.datasets[resultKey("crypto", set.ResultDatasetID)].Status)
}

func TestCreateTaskRejectsFactorResultSource(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	source := meta.datasets[resultKey("crypto", "dataset_prices")]
	source.Attributes = map[string]string{"dataset_role": storageio.DatasetRoleFactorResult}
	meta.datasets[resultKey("crypto", "dataset_prices")] = source
	svc := NewService(db, meta)

	_, err := svc.CreateTask(context.Background(), newTask())
	require.ErrorContains(t, err, "factor_result")
	_, err = db.GetTask(context.Background(), testTaskID())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestCreateTaskRejectsMissingFreq(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	set := newTask()
	set.Freq = ""

	_, err := svc.CreateTask(context.Background(), set)
	require.ErrorContains(t, err, "freq")
	require.Empty(t, meta.writeOps)
}

func TestDeleteSetKeepsResultDatasetUnlessPurging(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprintf("purge_%t", purge), func(t *testing.T) {
			db := openCatalogStore(t)
			meta := newMetadataFake()
			svc := NewService(db, meta)
			set, err := svc.CreateTask(context.Background(), newTask())
			require.NoError(t, err)
			require.NoError(t, db.SetTaskStatus(context.Background(), set.TaskID, domain.TaskStatusEnabled, domain.TaskStatusDisabled))

			require.NoError(t, svc.DeleteTask(context.Background(), set.TaskID, purge))
			_, err = db.GetTask(context.Background(), set.TaskID)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			dataset, datasetErr := meta.GetDataset(context.Background(), "crypto", set.ResultDatasetID)
			if purge {
				require.Error(t, datasetErr)
			} else {
				require.NoError(t, datasetErr)
				require.Equal(t, storageio.DatasetStatusActive, dataset.Status)
			}
		})
	}
}

// 删除失败后任务仍在，重试即可：先删结果数据集、再删数据库记录，Storage 删除数据集对"已不存在"幂等。
func TestDeleteTaskPurgeIsRetryable(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	set, err := svc.CreateTask(ctx, newTask())
	require.NoError(t, err)
	_, err = svc.SetTaskStatus(ctx, set.TaskID, domain.TaskStatusDisabled)
	require.NoError(t, err)
	meta.deleteErrs = 1

	err = svc.DeleteTask(ctx, set.TaskID, true)
	require.ErrorContains(t, err, "temporary metadata failure")
	stored, err := db.GetTask(ctx, set.TaskID)
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusDisabled, stored.Status, "没有持久化的删除意图，任务保持停用")

	require.NoError(t, svc.DeleteTask(ctx, set.TaskID, true))
	_, err = db.GetTask(ctx, set.TaskID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = meta.GetDataset(ctx, set.SpaceID, set.ResultDatasetID)
	require.Error(t, err)
}

// 数据集已在 Storage 删除、本地记录还在（删除数据集之后进程退出）：重试仍然成功。
func TestDeleteTaskPurgeAfterStorageMetadataWasAlreadyDeleted(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	set, err := svc.CreateTask(ctx, newTask())
	require.NoError(t, err)
	_, err = svc.SetTaskStatus(ctx, set.TaskID, domain.TaskStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, meta.DeleteDataset(ctx, set.SpaceID, set.ResultDatasetID))
	require.NoError(t, svc.DeleteTask(ctx, set.TaskID, true))
	_, err = db.GetTask(ctx, set.TaskID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// 已关联策略实例的任务不能删除。
func TestDeleteTaskRejectsBoundTask(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusDisabled)
	_, err := svc.BindTask(ctx, testTaskID(), "inst-1")
	require.NoError(t, err)
	require.NoError(t, svc.RemoveTaskMember(ctx, testTaskID(), "momentum"))
	_, err = svc.SetTaskStatus(ctx, testTaskID(), domain.TaskStatusDisabled)
	require.NoError(t, err)

	err = svc.DeleteTask(ctx, testTaskID(), true)
	require.ErrorIs(t, err, store.ErrConflict)
	require.ErrorContains(t, err, "inst-1")
	_, err = db.GetTask(ctx, testTaskID())
	require.NoError(t, err)
}

func TestCreateFactorDoesNotNeedTask(t *testing.T) {
	db := openCatalogStore(t)
	svc := NewService(db, newMetadataFake())

	factor := factorWithSpec("momentum", "output: any_column_name\n")
	created, err := svc.CreateFactor(context.Background(), factor)
	require.NoError(t, err)
	require.Equal(t, "momentum", created.FactorID)
	info, err := svc.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.Empty(t, info.Usages)
}

func TestCreateFactorRunsStaticValidation(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)

	_, err := svc.CreateFactor(context.Background(), factorWithSpec("momentum", "output: ts_mean(close,\n"))
	require.ErrorContains(t, err, "编译失败")
	_, err = db.GetFactor(context.Background(), "momentum")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// 输出名就是因子 ID，系统列不能当因子 ID 用。
	_, err = svc.CreateFactor(context.Background(), factorWithSpec("series_tag", "output: close\n"))
	require.ErrorContains(t, err, "系统列")
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	resultColumns, err := meta.ListColumns(context.Background(), "crypto", testResultDatasetID())
	require.NoError(t, err)
	require.NotContains(t, metadataColumnNames(resultColumns), "momentum")
}

func TestAddFactorToTaskValidatesAgainstSourceColumns(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	unknown := factorWithSpec("unknown_input", "output: missing\n")
	_, err := svc.CreateFactor(context.Background(), unknown)
	require.NoError(t, err)
	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "unknown_input", "", nil)
	require.ErrorContains(t, err, "unknown input column")

	// 单输出因子的输出名就是因子 ID：因子 ID 叫 close，输出列就与源列 close 重名。
	collide := factorWithSpec("close", "output: close * 2\n")
	_, err = svc.CreateFactor(context.Background(), collide)
	require.NoError(t, err)
	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "close", "", nil)
	require.ErrorContains(t, err, "与源列重名")
	_, err = db.GetMember(context.Background(), testTaskID(), "close")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestAddFactorToTaskRejectsPendingTask(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)

	pending := domain.FactorTask{
		TaskID: "ftask_pending_1m", SpaceID: "crypto", Name: "待创建任务", SourceDatasetID: "dataset_prices", Freq: "5m",
		ResultDatasetID: "dataset_factor_pending", Status: domain.TaskStatusPending,
	}
	require.NoError(t, db.CreateTask(context.Background(), pending))
	_, err = svc.AddTaskMember(context.Background(), pending.TaskID, "momentum", "", nil)
	require.ErrorContains(t, err, "not ready")
}

func TestAddFactorToTaskRejectsOutputOwnedByOtherMember(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	// 被移除的成员留下了结果列 momentum，它属于另一个成员（成员键在列属性 member_key 里）。
	retained := momentumColumn()
	retained.Attributes = map[string]string{"member_key": "gone_member"}
	key := resultKey("crypto", testResultDatasetID())
	meta.columns[key] = append(meta.columns[key], retained)
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)

	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "momentum", "", nil)
	require.ErrorContains(t, err, "已属于")
}

func TestAddFactorToTaskDoesNotAddColumnsOrSubmitRecalc(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	meta.writeOps = nil

	member, err := svc.AddTaskMember(context.Background(), testTaskID(), "momentum", "", nil)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, member.Status)
	require.Equal(t, "momentum", member.Factor.FactorID)
	require.Empty(t, meta.writeOps)
}

// 重新加入同一个成员键时核对旧列元数据：member_kind 或 factor_params 不一致就拒绝（兜底，正常不会发生）。
func TestReaddRejectsInconsistentRetainedColumnMetadata(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "pmean", domain.MemberKindFixed, map[string]float64{"window": 10})
	require.NoError(t, err)
	_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), "pmean__10", domain.MemberStatusEnabled)
	require.NoError(t, err)
	_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), "pmean__10", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, svc.RemoveTaskMember(ctx, testTaskID(), "pmean__10"))

	key := resultKey("crypto", testResultDatasetID())
	for i, col := range meta.columns[key] {
		if col.ColumnName == "pmean__10" {
			meta.columns[key][i].Attributes = map[string]string{"display_name": "pmean__10", "column_kind": "output", "factor_id": "pmean",
				"member_key": "pmean__10", "member_kind": "fixed", "output_name": "pmean", "factor_params": `{"window":11}`}
		}
	}
	_, err = svc.AddTaskMember(ctx, testTaskID(), "pmean", domain.MemberKindFixed, map[string]float64{"window": 10})
	require.ErrorContains(t, err, "不一致")
}

func TestReaddAfterRemoveReusesRetainedColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	_, err := svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, svc.RemoveTaskMember(context.Background(), testTaskID(), "momentum"))

	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "momentum", "", nil)
	require.NoError(t, err)
	_, err = svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusEnabled)
	require.NoError(t, err)
}

// 启用成员：重新校验、补齐结果列，不自动提交历史补算（需要历史因子值时手动 recalc）。
func TestEnableMemberRevalidatesAndAddsColumnsWithoutBackfill(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusDisabled)
	meta.writeOps = nil

	member, err := svc.SetTaskMemberStatus(ctx, testTaskID(), "momentum", domain.MemberStatusEnabled)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, member.Status)
	require.Equal(t, []string{"upsert_columns"}, meta.writeOps)
	jobs, err := db.ListRecalcJobs(ctx, store.RecalcJobFilter{TaskID: testTaskID()})
	require.NoError(t, err)
	require.Empty(t, jobs, "启用成员不能自动提交补算作业")
	var output storageio.ColumnInfo
	for _, col := range meta.columns[resultKey("crypto", testResultDatasetID())] {
		if col.ColumnName == "momentum" {
			output = col
		}
	}
	require.Equal(t, storageio.ColumnOriginCustom, output.OriginType)
	require.Empty(t, output.OriginID, "自定义列不带 origin_id")
	require.Equal(t, map[string]string{
		"display_name": "momentum", "column_kind": "output", "factor_id": "momentum", "member_key": "momentum", "member_kind": "fixed",
		"output_name": "momentum", "category_id": domain.DefaultCategoryID, "definition_hash": domain.DefinitionHash(domain.MustDefinition("momentum", "momentum", "output: ts_mean(close, 3)\n")),
		"factor_params": "{}",
	}, output.Attributes)
}

func TestEnableMemberRevalidatesAgainstCurrentSourceColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusDisabled)
	meta.sourceColumns = []storageio.ColumnInfo{sourceColumn("volume")}

	_, err := svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusEnabled)
	require.ErrorContains(t, err, "unknown input column")
	stored, err := db.GetMember(context.Background(), testTaskID(), "momentum")
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, stored.Status)
}

func TestDisableMemberKeepsColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	before := append([]storageio.ColumnInfo(nil), meta.columns[resultKey("crypto", testResultDatasetID())]...)
	meta.writeOps = nil

	member, err := svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, member.Status)
	require.Empty(t, meta.writeOps)
	require.Equal(t, before, meta.columns[resultKey("crypto", testResultDatasetID())])
}

func TestRemoveRequiresDisabledMember(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)

	err := svc.RemoveTaskMember(context.Background(), testTaskID(), "momentum")
	require.ErrorIs(t, err, store.ErrConflict)
	_, err = svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, svc.RemoveTaskMember(context.Background(), testTaskID(), "momentum"))
	_, err = svc.GetFactor(context.Background(), "momentum")
	require.NoError(t, err, "removing a member keeps the definition")
}

func TestSameDefinitionEnablesIndependentlyInTwoTasks(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	hourlySource := meta.datasets[resultKey("crypto", "dataset_prices")]
	hourlySource.DatasetID, hourlySource.Name, hourlySource.Freq = "dataset_prices_1h", "Hourly prices", "1h"
	meta.datasets[resultKey("crypto", hourlySource.DatasetID)] = hourlySource
	meta.sourceColumnsByDataset[hourlySource.DatasetID] = meta.sourceColumnsByDataset["dataset_prices"]
	hourly := newTask()
	hourly.TaskID, hourly.Name, hourly.SourceDatasetID, hourly.Freq = "ftask_prices_1h", "小时因子", hourlySource.DatasetID, "1h"
	_, err := svc.CreateTask(context.Background(), hourly)
	require.NoError(t, err)
	hourlyID := hourly.TaskID
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	_, err = svc.AddTaskMember(context.Background(), hourlyID, "momentum", "", nil)
	require.NoError(t, err)

	first, err := db.GetMember(context.Background(), testTaskID(), "momentum")
	require.NoError(t, err)
	second, err := db.GetMember(context.Background(), hourlyID, "momentum")
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, first.Status)
	require.Equal(t, domain.MemberStatusDisabled, second.Status)
	info, err := svc.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.Len(t, info.Usages, 2)
}

func TestUpdateFactorRejectedWhenAnyEnabledMember(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	factor := factorWithSpec("momentum", "output: ts_mean(close, 3) + 1\n")

	_, err := svc.UpdateFactor(context.Background(), factor)
	require.ErrorIs(t, err, store.ErrConflict)
	require.ErrorContains(t, err, "disable")
	got, err := db.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.NotContains(t, got.Spec, "+ 1")

	_, err = svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	updated, err := svc.UpdateFactor(context.Background(), factor)
	require.NoError(t, err)
	require.Contains(t, updated.Spec, "+ 1")
	require.Equal(t, domain.SpecHash(updated.Spec), updated.SpecHash)
}

func TestUpdateFactorRevalidatesAllReferencingTasksAndNamesTheFailingTask(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusDisabled)
	factor := factorWithSpec("momentum", "output: missing\n")

	_, err := svc.UpdateFactor(context.Background(), factor)
	require.ErrorContains(t, err, testTaskID())
	require.ErrorContains(t, err, "unknown input column")
	got, err := db.GetFactor(context.Background(), "momentum")
	require.NoError(t, err)
	require.Equal(t, []string{"close"}, got.InputColumns)
}

func TestDeleteFactorRejectedWhileReferenced(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusDisabled)

	err := svc.DeleteFactor(context.Background(), "momentum")
	require.ErrorIs(t, err, store.ErrConflict)
	require.NoError(t, svc.RemoveTaskMember(context.Background(), testTaskID(), "momentum"))
	require.NoError(t, svc.DeleteFactor(context.Background(), "momentum"))
}

func TestDeleteTaskRejectedWhileMembersExist(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, db.SetTaskStatus(context.Background(), testTaskID(), domain.TaskStatusEnabled, domain.TaskStatusDisabled))

	err := svc.DeleteTask(context.Background(), testTaskID(), false)
	require.ErrorContains(t, err, "members")
}

func TestListFactorsWithAndWithoutTask(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	loose := factorWithSpec("loose", "output: close\n")
	_, err := svc.CreateFactor(context.Background(), loose)
	require.NoError(t, err)

	all, err := svc.ListFactors(context.Background(), "", "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Empty(t, all[0].Usages) // loose sorts first
	require.Equal(t, []domain.FactorUsage{{TaskID: testTaskID(), MemberKey: "momentum", Status: domain.MemberStatusEnabled, Params: map[string]float64(nil)}}, all[1].Usages)

	inSet, err := svc.ListFactors(context.Background(), testTaskID(), domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.Empty(t, inSet)
	inSet, err = svc.ListFactors(context.Background(), testTaskID(), domain.MemberStatusEnabled)
	require.NoError(t, err)
	require.Len(t, inSet, 1)
}

func TestAddAndUpdateDoNotInterleave(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "momentum", "", nil)
	require.NoError(t, err)
	unlock, err := svc.Locks().LockFactorContext(context.Background(), "momentum")
	require.NoError(t, err)
	done := make(chan error, 3)
	go func() {
		_, err := svc.AddTaskMember(context.Background(), testTaskID(), "momentum", "", nil)
		done <- err
	}()
	go func() { _, err := svc.UpdateFactor(context.Background(), testFactor()); done <- err }()
	go func() {
		_, err := svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusDisabled)
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("member or definition operation did not wait for the definition lock")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("operation remained blocked after releasing the definition lock")
		}
	}
}

func TestReconcileTaskAddsNewSourceColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("volume"))
	meta.writeOps = nil

	require.NoError(t, svc.ReconcileTask(context.Background(), testTaskID()))
	require.Equal(t, []string{"upsert_columns"}, meta.writeOps)
	require.ElementsMatch(t, []storageio.ColumnInfo{sourceColumn("close"), sourceColumn("volume"), paramsColumn()}, meta.columns[resultKey("crypto", testResultDatasetID())])
}

func TestReconcileRejectsSourceColumnCollisionWithEnabledMember(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("momentum"))
	meta.writeOps = nil

	err := svc.ReconcileTask(context.Background(), testTaskID())
	require.ErrorContains(t, err, `source column "momentum" collides with enabled member momentum`)
	require.Empty(t, meta.writeOps)
}

func TestReconcileContinuesAfterOneTaskFailure(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	secondSource := meta.datasets[resultKey("crypto", "dataset_zzz")]
	secondSource = storageio.DatasetInfo{
		SpaceID: "crypto", DatasetID: "dataset_zzz", DataSourceID: "other", DataNodeID: "storage-node-0",
		Name: "Other", DataKind: storageio.DataKindTimeSeries, Freq: "1m", Retention: "720h",
		Status: storageio.DatasetStatusActive, Attributes: map[string]string{},
	}
	meta.datasets[resultKey("crypto", "dataset_zzz")] = secondSource
	meta.columns[resultKey("crypto", "dataset_zzz")] = []storageio.ColumnInfo{sourceColumn("close")}
	meta.sourceColumnsByDataset = map[string][]storageio.ColumnInfo{"dataset_zzz": {sourceColumn("close")}}
	svc := NewService(db, meta)
	first, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	secondInput := newTask()
	secondInput.SourceDatasetID = "dataset_zzz"
	secondInput.TaskID = ""
	secondInput.Name = "第二任务"
	secondInput.ResultDatasetID = ""
	second, err := svc.CreateTask(context.Background(), secondInput)
	require.NoError(t, err)
	meta.datasets[resultKey("crypto", first.SourceDatasetID)] = storageio.DatasetInfo{}
	meta.sourceColumnsByDataset[second.SourceDatasetID] = []storageio.ColumnInfo{sourceColumn("close"), sourceColumn("volume")}
	meta.writeOps = nil

	err = svc.Reconcile(context.Background())
	require.ErrorContains(t, err, "reconcile factor task")
	columns, err := meta.ListColumns(context.Background(), "crypto", second.ResultDatasetID)
	require.NoError(t, err)
	require.Contains(t, metadataColumnNames(columns), "volume")
}

func TestLifecycleOpsSerializeWithPeriodLock(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	unlock := svc.Locks().Lock(testTaskID())
	done := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		defer close(done)
		_, _ = svc.SetTaskStatus(context.Background(), testTaskID(), domain.TaskStatusDisabled)
	}()
	<-started
	select {
	case <-done:
		t.Fatal("lifecycle operation did not wait for the shared set lock")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lifecycle operation remained blocked after releasing the set lock")
	}
}

func openCatalogStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func newReadyService(t *testing.T, db *store.Store, meta *metadataFake) *Service {
	t.Helper()
	svc := NewService(db, meta)
	_, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	return svc
}

func newTask() domain.FactorTask {
	return domain.FactorTask{
		TaskID: "ftask_prices_1m", SpaceID: "crypto", Name: "价格因子", SourceDatasetID: "dataset_prices", Freq: "1m",
	}
}

func testFactor() domain.FactorDef {
	return factorWithSpec("momentum", "output: ts_mean(close, 3)\n")
}

// factorWithSpec 返回一个未规范化的请求定义（与接口收到的一样），输出列名由 spec 决定。
func factorWithSpec(factorID, spec string) domain.FactorDef {
	return domain.FactorDef{FactorID: factorID, Name: factorID, Spec: spec}
}

func testTaskID() string { return "ftask_prices_1m" }

func testResultDatasetID() string { return "dataset_factor_prices_1m" }

// momentumColumn 是测试因子 momentum 的固定成员输出列。
func momentumColumn() storageio.ColumnInfo {
	member := domain.MemberDef{Key: "momentum", Factor: domain.MustDefinition("momentum", "momentum", "output: ts_mean(close, 3)\n"), Kind: domain.MemberKindFixed}
	return factorOutputColumns(member)[0]
}

// addMember registers the standard test factor (if missing) and attaches it to
// the set, enabling it when status asks for it.
func addMember(t *testing.T, svc *Service, taskID, factorID, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.db.GetFactor(ctx, factorID); err != nil {
		_, err = svc.CreateFactor(ctx, factorWithSpec(factorID, "output: ts_mean(close, 3)\n"))
		require.NoError(t, err)
	}
	_, err := svc.AddTaskMember(ctx, taskID, factorID, "", nil)
	require.NoError(t, err)
	if status == domain.MemberStatusEnabled {
		_, err = svc.SetTaskMemberStatus(ctx, taskID, factorID, domain.MemberStatusEnabled)
		require.NoError(t, err)
	}
}

func factorIDs(factors []domain.FactorDef) []string {
	ids := make([]string, len(factors))
	for i, factor := range factors {
		ids[i] = factor.FactorID
	}
	return ids
}

func metadataColumnNames(columns []storageio.ColumnInfo) []string {
	names := make([]string, len(columns))
	for i, col := range columns {
		names[i] = col.ColumnName
	}
	return names
}

func resultKey(spaceID, datasetID string) string { return spaceID + "/" + datasetID }

func sourceColumn(name string) storageio.ColumnInfo {
	return storageio.ColumnInfo{ColumnName: name, OriginType: storageio.ColumnOriginField, OriginID: name, ValueType: storageio.ColumnTypeDouble, Status: storageio.ColumnStatusActive, Attributes: map[string]string{"display_name": "收盘价"}}
}

type metadataFake struct {
	mu                     sync.Mutex
	datasets               map[string]storageio.DatasetInfo
	columns                map[string][]storageio.ColumnInfo
	sourceColumns          []storageio.ColumnInfo
	sourceColumnsByDataset map[string][]storageio.ColumnInfo
	createdSpec            storageio.ResultDatasetSpec
	createCalls            int
	createdDatasets        int
	activateErrs           int
	deleteErrs             int
	getDatasetErr          error
	writeOps               []string
}

func newMetadataFake() *metadataFake {
	source := storageio.DatasetInfo{
		SpaceID: "crypto", DatasetID: "dataset_prices", DataSourceID: "binance", DataNodeID: "storage-node-0",
		Name: "Prices", Description: "Price bars", DataKind: storageio.DataKindTimeSeries, Freq: "1m", Status: storageio.DatasetStatusActive, Attributes: map[string]string{},
	}
	cols := []storageio.ColumnInfo{sourceColumn("close"), {
		ColumnName: "subject_id", OriginType: storageio.ColumnOriginSystem, ValueType: storageio.ColumnTypeString, Status: storageio.ColumnStatusActive,
	}}
	return &metadataFake{
		datasets: map[string]storageio.DatasetInfo{resultKey("crypto", "dataset_prices"): source},
		columns:  map[string][]storageio.ColumnInfo{}, sourceColumns: cols,
		sourceColumnsByDataset: map[string][]storageio.ColumnInfo{"dataset_prices": cols},
	}
}

func (f *metadataFake) GetDataset(_ context.Context, spaceID, datasetID string) (storageio.DatasetInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getDatasetErr != nil {
		return storageio.DatasetInfo{}, f.getDatasetErr
	}
	dataset, ok := f.datasets[resultKey(spaceID, datasetID)]
	if !ok {
		return storageio.DatasetInfo{}, errors.New("dataset not found")
	}
	return dataset, nil
}

func (f *metadataFake) ListColumns(_ context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if datasetID == "dataset_prices" {
		return append([]storageio.ColumnInfo(nil), f.sourceColumns...), nil
	}
	if columns, ok := f.sourceColumnsByDataset[datasetID]; ok {
		return append([]storageio.ColumnInfo(nil), columns...), nil
	}
	return append([]storageio.ColumnInfo(nil), f.columns[resultKey(spaceID, datasetID)]...), nil
}

func (f *metadataFake) CreateResultDataset(_ context.Context, spec storageio.ResultDatasetSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.writeOps = append(f.writeOps, "create")
	f.createdSpec = spec
	key := resultKey(spec.SpaceID, spec.DatasetID)
	if _, exists := f.datasets[key]; !exists {
		// 与 Storage 一致：数据集名称在空间内唯一，且含汉字、不超过 10 个字。
		if err := validateStorageDatasetName(spec.Name); err != nil {
			return err
		}
		for _, other := range f.datasets {
			if other.SpaceID == spec.SpaceID && other.Name == spec.Name {
				return fmt.Errorf("dataset name %q already exists in space %s", spec.Name, spec.SpaceID)
			}
		}
		for _, column := range spec.Columns {
			if err := validateResultColumnContract(column); err != nil {
				return err
			}
		}
		f.createdDatasets++
		attrs := map[string]string{"dataset_role": storageio.DatasetRoleFactorResult, "source_dataset_id": spec.SourceDatasetID}
		for name, value := range spec.Attributes {
			attrs[name] = value
		}
		f.datasets[key] = storageio.DatasetInfo{
			SpaceID: spec.SpaceID, DatasetID: spec.DatasetID, DataSourceID: spec.DataSourceID, DataNodeID: spec.DataNodeID,
			Name: spec.Name, Description: spec.Description, DataKind: spec.DataKind, Freq: spec.Frequency, Retention: "720h", Status: storageio.DatasetStatusDisabled, Attributes: attrs,
		}
		f.columns[key] = append([]storageio.ColumnInfo(nil), spec.Columns...)
	}
	return nil
}

func (f *metadataFake) SetDatasetSubjectTags(_ context.Context, spaceID, datasetID string, tags []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeOps = append(f.writeOps, "subject_tags")
	key := resultKey(spaceID, datasetID)
	dataset := f.datasets[key]
	dataset.SubjectTags = append([]string(nil), tags...)
	f.datasets[key] = dataset
	return nil
}

func (f *metadataFake) UpsertColumns(_ context.Context, spaceID, datasetID string, cols []storageio.ColumnInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeOps = append(f.writeOps, "upsert_columns")
	key := resultKey(spaceID, datasetID)
	current := make(map[string]storageio.ColumnInfo, len(f.columns[key]))
	for _, col := range f.columns[key] {
		current[col.ColumnName] = col
	}
	for _, col := range cols {
		if err := validateResultColumnContract(col); err != nil {
			return err
		}
		current[col.ColumnName] = col
	}
	merged := make([]storageio.ColumnInfo, 0, len(current))
	for _, col := range current {
		merged = append(merged, col)
	}
	f.columns[key] = merged
	return nil
}

func (f *metadataFake) ActivateDataset(_ context.Context, spaceID, datasetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeOps = append(f.writeOps, "activate")
	if f.activateErrs > 0 {
		f.activateErrs--
		return errors.New("activate failed")
	}
	dataset := f.datasets[resultKey(spaceID, datasetID)]
	dataset.Status = storageio.DatasetStatusActive
	f.datasets[resultKey(spaceID, datasetID)] = dataset
	return nil
}

func (f *metadataFake) DeleteDataset(_ context.Context, spaceID, datasetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeOps = append(f.writeOps, "delete")
	if f.deleteErrs > 0 {
		f.deleteErrs--
		return errors.New("temporary metadata failure")
	}
	delete(f.datasets, resultKey(spaceID, datasetID))
	delete(f.columns, resultKey(spaceID, datasetID))
	return nil
}

func TestReconcileRecordsResultReady(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	ready := map[string]bool{}
	svc := NewService(db, meta, WithReadyRecorder(func(taskID string, ok bool) { ready[taskID] = ok }))
	set, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	require.True(t, ready[set.TaskID], "a freshly created set is ready")

	addMember(t, svc, set.TaskID, "momentum", domain.MemberStatusEnabled)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("momentum"))
	require.Error(t, svc.ReconcileTask(context.Background(), set.TaskID))
	require.False(t, ready[set.TaskID], "a failed reconciliation marks the set not ready")

	meta.sourceColumns = meta.sourceColumns[:len(meta.sourceColumns)-1]
	require.NoError(t, svc.ReconcileTask(context.Background(), set.TaskID))
	require.True(t, ready[set.TaskID])
}

func TestReconcileKeepsReadinessOnStorageOutage(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	ready := map[string]bool{}
	svc := NewService(db, meta, WithReadyRecorder(func(taskID string, ok bool) { ready[taskID] = ok }))
	set, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	require.True(t, ready[set.TaskID])

	meta.getDatasetErr = fmt.Errorf("%w: connection refused", storageio.ErrInfra)
	require.Error(t, svc.ReconcileTask(context.Background(), set.TaskID))

	require.True(t, ready[set.TaskID], "a transient Storage failure keeps the set ready")
}

func TestReconcileFollowsSourceSubjectTags(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	ready := map[string]bool{}
	svc := NewService(db, meta, WithReadyRecorder(func(taskID string, ok bool) { ready[taskID] = ok }))
	set, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	source := meta.datasets[resultKey("crypto", "dataset_prices")]
	source.SubjectTags = []string{"binance_spot", "binance_swap"}
	meta.datasets[resultKey("crypto", "dataset_prices")] = source
	meta.writeOps = nil

	require.NoError(t, svc.ReconcileTask(context.Background(), set.TaskID))

	require.True(t, ready[set.TaskID], "a source subject-tag change must not halt the set")
	require.Equal(t, []string{"subject_tags"}, meta.writeOps)
	require.Equal(t, []string{"binance_spot", "binance_swap"}, meta.datasets[resultKey("crypto", set.ResultDatasetID)].SubjectTags)
}

// 结果列身份一致但属性过期（列上还留着旧的 factor_id、分类）：对账刷新属性并保留别处设置的其他属性。
func TestReconcileRefreshesStaleResultColumnAttributes(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	key := resultKey("crypto", testResultDatasetID())
	columns := meta.columns[key]
	stale := -1
	for i, col := range columns {
		if col.ColumnName == "momentum" {
			stale = i
		}
	}
	require.GreaterOrEqual(t, stale, 0)
	want := columns[stale].Attributes["factor_id"]
	require.NotEmpty(t, want)
	attributes := map[string]string{}
	for k, v := range columns[stale].Attributes {
		attributes[k] = v
	}
	attributes["factor_id"] = "旧定义"
	attributes["note"] = "别处设置的属性"
	columns[stale].Attributes = attributes
	meta.writeOps = nil

	require.NoError(t, svc.ReconcileTask(ctx, testTaskID()))

	require.Equal(t, []string{"upsert_columns"}, meta.writeOps)
	for _, col := range meta.columns[key] {
		if col.ColumnName == "momentum" {
			require.Equal(t, want, col.Attributes["factor_id"])
			require.Equal(t, "别处设置的属性", col.Attributes["note"], "只覆盖期望里写出的键")
		}
	}
	meta.writeOps = nil
	require.NoError(t, svc.ReconcileTask(ctx, testTaskID()))
	require.Empty(t, meta.writeOps, "属性已是最新时不再写 Storage")
}

// validateStorageDatasetName 复刻 Storage 对数据集名称的要求：含汉字、不超过 10 个字。
func validateStorageDatasetName(name string) error {
	if err := domain.ValidateTaskName(name); err != nil {
		return fmt.Errorf("storage 拒绝数据集名称 %q：%w", name, err)
	}
	return nil
}

// validateResultColumnContract 检查因子结果列的命名与类型契约（Storage 只校验通用规则，契约由 factor-mgr 自己保证）：
// 来源都是 custom 且不带 origin_id，输出列名是成员键或 成员键__输出名、类型 double，__params 类型 json。
func validateResultColumnContract(col storageio.ColumnInfo) error {
	if col.OriginType != storageio.ColumnOriginCustom {
		return nil
	}
	attrs := col.Attributes
	switch attrs["column_kind"] {
	case "params":
		if col.ColumnName != "__params" || col.ValueType != storageio.ColumnTypeJSON {
			return fmt.Errorf("params 列 %q 必须叫 __params 且是 json", col.ColumnName)
		}
		if attrs["display_name"] == "" {
			return fmt.Errorf("__params 需要展示名")
		}
		return nil
	case "output":
	default:
		return fmt.Errorf("因子列契约要求因子列带 column_kind，列 %q 得到 %q", col.ColumnName, attrs["column_kind"])
	}
	factorID, memberKey, output, kind := attrs["factor_id"], attrs["member_key"], attrs["output_name"], attrs["member_kind"]
	if factorID == "" || memberKey == "" || output == "" {
		return fmt.Errorf("因子列契约要求输出列 %q 带 factor_id、member_key、output_name", col.ColumnName)
	}
	if kind != "fixed" && kind != "var" {
		return fmt.Errorf("因子列契约要求 member_kind 是 fixed 或 var，列 %q 得到 %q", col.ColumnName, kind)
	}
	if memberKey != factorID && !strings.HasPrefix(memberKey, factorID+"__") {
		return fmt.Errorf("因子列契约要求 member_key %q 以 factor_id %q 开头", memberKey, factorID)
	}
	want := memberKey
	if output != factorID {
		want = memberKey + "__" + output
	}
	if col.ColumnName != want {
		return fmt.Errorf("因子列契约要求输出列名是 %q，得到 %q", want, col.ColumnName)
	}
	if col.OriginID != "" {
		return fmt.Errorf("自定义列 %q 不带 origin_id，得到 %q", col.ColumnName, col.OriginID)
	}
	if col.ValueType != storageio.ColumnTypeDouble {
		return fmt.Errorf("因子列契约要求输出列 %q 是 double", col.ColumnName)
	}
	if attrs["display_name"] != col.ColumnName {
		return fmt.Errorf("因子列契约要求输出列 %q 的展示名等于列名，得到 %q", col.ColumnName, attrs["display_name"])
	}
	return nil
}

