package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	require.Equal(t, domain.TaskID("dataset_prices", "1m"), set.TaskID)
	require.Equal(t, domain.ResultDatasetID(domain.TaskID("dataset_prices", "1m")), set.ResultDatasetID)
	require.Equal(t, domain.TaskStatusEnabled, set.Status)
	require.Equal(t, "storage-node-0", meta.createdSpec.DataNodeID)
	require.Equal(t, storageio.DatasetRoleFactorResult, meta.createdSpec.Attributes["dataset_role"])
	require.Equal(t, "factor", meta.createdSpec.Attributes["owner_module"])
	require.Equal(t, "factor", meta.createdSpec.Attributes["write_owner"])
	require.Equal(t, []storageio.ColumnInfo{sourceColumn("close")}, meta.createdSpec.Columns)
	require.Equal(t, "Prices因子", meta.createdSpec.Name)
	require.LessOrEqual(t, len([]rune(meta.createdSpec.Name)), 10)
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
	require.Equal(t, "ftask_prices_1h", created.TaskID)
	require.Equal(t, "dataset_factor_prices_1h", created.ResultDatasetID)
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
	set, err := db.GetTask(context.Background(), domain.TaskID("dataset_prices", "1m"))
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
	_, err = db.GetTask(context.Background(), domain.TaskID("dataset_prices", "1m"))
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

func TestReconcileResumesDurableResultDatasetPurge(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	set, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	require.NoError(t, db.SetTaskStatus(context.Background(), set.TaskID, domain.TaskStatusEnabled, domain.TaskStatusDisabled))
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	meta.deleteErrs = 1

	err = svc.DeleteTask(context.Background(), set.TaskID, true)
	require.ErrorContains(t, err, "temporary metadata failure")
	pending, err := db.GetTask(context.Background(), set.TaskID)
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusDeleting, pending.Status)
	_, err = svc.SetTaskStatus(context.Background(), set.TaskID, domain.TaskStatusEnabled)
	require.ErrorContains(t, err, "purge is pending")
	_, err = svc.AddTaskMember(context.Background(), set.TaskID, "", "momentum", nil)
	require.ErrorContains(t, err, "purge is pending")

	// Reconcile runs at startup and periodically. It resumes after failures at
	// any point after the durable intent, including a crash after physical purge.
	require.NoError(t, svc.Reconcile(context.Background()))
	_, err = db.GetTask(context.Background(), set.TaskID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = meta.GetDataset(context.Background(), set.SpaceID, set.ResultDatasetID)
	require.Error(t, err)
}

func TestDeletingTaskRejectsLifecycleMutation(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	set, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	require.NoError(t, db.SetTaskStatus(context.Background(), set.TaskID, domain.TaskStatusEnabled, domain.TaskStatusDisabled))
	require.NoError(t, db.SetTaskStatus(context.Background(), set.TaskID, domain.TaskStatusDisabled, domain.TaskStatusDeleting))

	retry := set
	retry.Status = domain.TaskStatusPending
	_, err = svc.CreateTask(context.Background(), retry)
	require.ErrorContains(t, err, "purge is pending")
	_, err = svc.SetTaskStatus(context.Background(), set.TaskID, domain.TaskStatusDisabled)
	require.ErrorContains(t, err, "purge is pending")

	// Even if an invariant-breaking writer inserted a member, member mutations
	// still cannot alter the pending purge into an enabled or stuck state.
	factor, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	_, err = svc.AddTaskMember(context.Background(), set.TaskID, "", factor.FactorID, nil)
	require.ErrorContains(t, err, "purge is pending")
	_, err = db.AddMember(context.Background(), set.TaskID, factor.FactorID, factor.FactorID, nil)
	require.NoError(t, err)
	_, err = svc.SetTaskMemberStatus(context.Background(), set.TaskID, factor.FactorID, domain.MemberStatusEnabled)
	require.ErrorContains(t, err, "purge is pending")
	err = svc.RemoveTaskMember(context.Background(), set.TaskID, factor.FactorID)
	require.ErrorContains(t, err, "purge is pending")
	stored, err := db.GetTask(context.Background(), set.TaskID)
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusDeleting, stored.Status)
}

func TestReconcileFinishesPurgeAfterStorageMetadataWasAlreadyDeleted(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	set, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	require.NoError(t, db.SetTaskStatus(context.Background(), set.TaskID, domain.TaskStatusEnabled, domain.TaskStatusDisabled))
	require.NoError(t, db.SetTaskStatus(context.Background(), set.TaskID, domain.TaskStatusDisabled, domain.TaskStatusDeleting))
	// Models process death after Storage's metadata commit and before the local
	// Factor task row is removed. DeleteDataset is idempotent for the retry.
	require.NoError(t, meta.DeleteDataset(context.Background(), set.SpaceID, set.ResultDatasetID))
	require.NoError(t, svc.Reconcile(context.Background()))
	_, err = db.GetTask(context.Background(), set.TaskID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
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
	resultColumns, err := meta.ListColumns(context.Background(), "crypto", domain.ResultDatasetID(domain.TaskID("dataset_prices", "1m")))
	require.NoError(t, err)
	require.NotContains(t, metadataColumnNames(resultColumns), "momentum")
}

func TestAddFactorToTaskValidatesAgainstSourceColumns(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	unknown := factorWithSpec("unknown_input", "output: missing\n")
	_, err := svc.CreateFactor(context.Background(), unknown)
	require.NoError(t, err)
	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "", "unknown_input", nil)
	require.ErrorContains(t, err, "unknown input column")

	// 单输出因子的输出名就是因子 ID：因子 ID 叫 close，输出列就与源列 close 重名。
	collide := factorWithSpec("close", "output: close * 2\n")
	_, err = svc.CreateFactor(context.Background(), collide)
	require.NoError(t, err)
	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "", "close", nil)
	require.ErrorContains(t, err, "与源列重名")
	_, err = db.GetMember(context.Background(), testTaskID(), "close")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestAddFactorToTaskRejectsPendingOrDeletingTask(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	require.NoError(t, db.SetTaskStatus(context.Background(), testTaskID(), domain.TaskStatusEnabled, domain.TaskStatusDisabled))
	require.NoError(t, db.SetTaskStatus(context.Background(), testTaskID(), domain.TaskStatusDisabled, domain.TaskStatusDeleting))
	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "", "momentum", nil)
	require.ErrorContains(t, err, "purge is pending")

	pending := domain.FactorTask{
		TaskID: "ftask_pending_1m", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "5m",
		ResultDatasetID: "dataset_factor_pending", Status: domain.TaskStatusPending,
	}
	require.NoError(t, db.CreateTask(context.Background(), pending))
	_, err = svc.AddTaskMember(context.Background(), pending.TaskID, "", "momentum", nil)
	require.ErrorContains(t, err, "not ready")
}

func TestAddFactorToTaskRejectsOutputOwnedByOtherFactor(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	// other 是两个输出的因子：被移除的 other 成员留下了结果列 momentum 与 extra，它们属于 other。
	other := factorWithSpec("other", "outputs:\n  momentum: ts_mean(close, 3)\n  extra: ts_mean(close, 5)\n")
	other.Name = "other"
	_, err := svc.CreateFactor(context.Background(), other)
	require.NoError(t, err)
	// A retained result column from a removed "other" member.
	require.NoError(t, meta.UpsertColumns(context.Background(), "crypto", domain.ResultDatasetID(domain.TaskID("dataset_prices", "1m")),
		appendFactorColumns(nil, domain.MemberDef{Alias: other.FactorID, Factor: domain.MustDefinition(other.FactorID, other.Name, other.Spec)})))
	_, err = svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)

	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "", "momentum", nil)
	require.ErrorContains(t, err, "已属于")
}

func TestAddFactorToTaskDoesNotAddColumnsOrSubmitRecalc(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	_, err := svc.CreateFactor(context.Background(), testFactor())
	require.NoError(t, err)
	meta.writeOps = nil

	member, err := svc.AddTaskMember(context.Background(), testTaskID(), "", "momentum", nil)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, member.Status)
	require.Equal(t, "momentum", member.Factor.FactorID)
	require.Empty(t, meta.writeOps)
}

func TestReaddAfterRemoveReusesRetainedColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	_, err := svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, svc.RemoveTaskMember(context.Background(), testTaskID(), "momentum"))

	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "", "momentum", nil)
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
	for _, col := range meta.columns[resultKey("crypto", domain.ResultDatasetID(domain.TaskID("dataset_prices", "1m")))] {
		if col.ColumnName == "momentum" {
			output = col
		}
	}
	require.Equal(t, storageio.ColumnOriginFactor, output.OriginType)
	require.Equal(t, map[string]string{
		"display_name": "momentum", "factor_output": "momentum", "origin_factor_id": "momentum",
		"member_alias": "momentum", "factor_id": "momentum", "category_id": domain.DefaultCategoryID, "column_kind": ColumnKindOutput, "output_name": "momentum",
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
	before := append([]storageio.ColumnInfo(nil), meta.columns[resultKey("crypto", domain.ResultDatasetID(domain.TaskID("dataset_prices", "1m")))]...)
	meta.writeOps = nil

	member, err := svc.SetTaskMemberStatus(context.Background(), testTaskID(), "momentum", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusDisabled, member.Status)
	require.Empty(t, meta.writeOps)
	require.Equal(t, before, meta.columns[resultKey("crypto", domain.ResultDatasetID(domain.TaskID("dataset_prices", "1m")))])
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
	hourly.SourceDatasetID, hourly.Freq = hourlySource.DatasetID, "1h"
	_, err := svc.CreateTask(context.Background(), hourly)
	require.NoError(t, err)
	hourlyID := domain.TaskID(hourlySource.DatasetID, "1h")
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	_, err = svc.AddTaskMember(context.Background(), hourlyID, "", "momentum", nil)
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
	require.Equal(t, []domain.FactorUsage{{TaskID: testTaskID(), MemberAlias: "momentum", Status: domain.MemberStatusEnabled}}, all[1].Usages)

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
	_, err = svc.AddTaskMember(context.Background(), testTaskID(), "", "momentum", nil)
	require.NoError(t, err)
	unlock, err := svc.Locks().LockFactorContext(context.Background(), "momentum")
	require.NoError(t, err)
	done := make(chan error, 3)
	go func() {
		_, err := svc.AddTaskMember(context.Background(), testTaskID(), "slow", "momentum", nil)
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

	require.NoError(t, svc.ReconcileTask(context.Background(), domain.TaskID("dataset_prices", "1m")))
	require.Equal(t, []string{"upsert_columns"}, meta.writeOps)
	require.ElementsMatch(t, []storageio.ColumnInfo{sourceColumn("close"), sourceColumn("volume")}, meta.columns[resultKey("crypto", domain.ResultDatasetID(domain.TaskID("dataset_prices", "1m")))])
}

func TestReconcileRejectsSourceColumnCollisionWithEnabledMember(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	meta.sourceColumns = append(meta.sourceColumns, sourceColumn("momentum"))
	meta.writeOps = nil

	err := svc.ReconcileTask(context.Background(), domain.TaskID("dataset_prices", "1m"))
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
	unlock := svc.Locks().Lock(domain.TaskID("dataset_prices", "1m"))
	done := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		defer close(done)
		_, _ = svc.SetTaskStatus(context.Background(), domain.TaskID("dataset_prices", "1m"), domain.TaskStatusDisabled)
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
		SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m",
	}
}

func testFactor() domain.FactorDef {
	return factorWithSpec("momentum", "output: ts_mean(close, 3)\n")
}

// factorWithSpec 返回一个未规范化的请求定义（与接口收到的一样），输出列名由 spec 决定。
func factorWithSpec(factorID, spec string) domain.FactorDef {
	return domain.FactorDef{FactorID: factorID, Name: factorID, Spec: spec}
}

func testTaskID() string { return domain.TaskID("dataset_prices", "1m") }

// addMember registers the standard test factor (if missing) and attaches it to
// the set, enabling it when status asks for it.
func addMember(t *testing.T, svc *Service, taskID, factorID, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.db.GetFactor(ctx, factorID); err != nil {
		_, err = svc.CreateFactor(ctx, factorWithSpec(factorID, "output: ts_mean(close, 3)\n"))
		require.NoError(t, err)
	}
	_, err := svc.AddTaskMember(ctx, taskID, "", factorID, nil)
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

func TestResultDatasetNameIsDerivedFromItsSource(t *testing.T) {
	require.Equal(t, "现货分钟K线因子", resultDatasetName("现货分钟K线"))
	require.NotEqual(t, resultDatasetName("现货分钟K线"), resultDatasetName("合约分钟K线"), "factor tasks of different sources must not collide")
	long := resultDatasetName("一二三四五六七八九十")
	require.Equal(t, "一二三四五六七八因子", long)
	require.LessOrEqual(t, len([]rune(long)), maxResultDatasetNameRunes)
	require.Equal(t, "因子结果", resultDatasetName(" "))
}

// 结果数据集清理失败时参数冻结历史还在：参数记录在数据集清理成功之后才删，任务停在 deleting 可以重试。
func TestDeleteTaskKeepsParamHistoryWhenPurgeFails(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := NewService(db, meta)
	set, err := svc.CreateTask(ctx, newTask())
	require.NoError(t, err)
	require.NoError(t, db.SetTaskStatus(ctx, set.TaskID, domain.TaskStatusEnabled, domain.TaskStatusDisabled))
	at := time.Unix(1_800_000_000, 0).UTC()
	_, err = db.FreezeParams(ctx, set.TaskID, at, at.Add(-time.Minute),
		[]store.FreezeMember{{Alias: "fast", Defaults: domain.ParamSnapshot{"window": {Default: 20}}}})
	require.NoError(t, err)
	meta.deleteErrs = 1

	require.Error(t, svc.DeleteTask(ctx, set.TaskID, true))
	intervals, err := db.ListParamIntervals(ctx, set.TaskID, "", at.Add(-time.Hour), at.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, intervals, 1, "清理失败时冻结历史必须保留")

	require.NoError(t, svc.Reconcile(ctx))
	intervals, err = db.ListParamIntervals(ctx, set.TaskID, "", at.Add(-time.Hour), at.Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, intervals)
}

// 结果列身份一致但属性过期（成员换绑了另一个定义后列上还留着旧的 factor_id、分类）：对账刷新属性并保留别处设置的其他属性。
func TestReconcileRefreshesStaleResultColumnAttributes(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	key := resultKey("crypto", domain.ResultDatasetID(testTaskID()))
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
