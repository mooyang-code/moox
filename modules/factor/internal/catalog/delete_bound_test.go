package catalog

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// boundTaskWithEnabledMember 建出一个已启用、关联到 inst-1、带一个已启用成员的任务。
func boundTaskWithEnabledMember(t *testing.T) (*Service, *store.Store, *metadataFake) {
	t.Helper()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	_, err := svc.BindTask(context.Background(), testTaskID(), "inst-1")
	require.NoError(t, err)
	return svc, db, meta
}

// 关联的任务连同已启用的成员、结果数据集一起删除；重复调用仍然成功。
func TestDeleteBoundTaskRemovesMembersDatasetAndTask(t *testing.T) {
	ctx := context.Background()
	svc, db, meta := boundTaskWithEnabledMember(t)
	task, err := db.GetTask(ctx, testTaskID())
	require.NoError(t, err)

	require.NoError(t, svc.DeleteBoundTask(ctx, testTaskID(), "inst-1"))
	_, err = db.GetTask(ctx, testTaskID())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	members, err := db.ListMembers(ctx, testTaskID(), "")
	require.NoError(t, err)
	require.Empty(t, members)
	_, err = meta.GetDataset(ctx, task.SpaceID, task.ResultDatasetID)
	require.Error(t, err, "结果数据集已删除")
	// 定义本身保留：只删成员，不删因子定义。
	_, err = db.GetFactor(ctx, "momentum")
	require.NoError(t, err)

	require.NoError(t, svc.DeleteBoundTask(ctx, testTaskID(), "inst-1"), "任务已经不在了，重试算成功")
}

// 删除结果数据集失败时任务已停用、成员已删除，记录还在；重试接着做完。
func TestDeleteBoundTaskIsRetryableAfterPurgeFailure(t *testing.T) {
	ctx := context.Background()
	svc, db, meta := boundTaskWithEnabledMember(t)
	meta.deleteErrs = 1

	err := svc.DeleteBoundTask(ctx, testTaskID(), "inst-1")
	require.ErrorContains(t, err, "temporary metadata failure")
	stored, err := db.GetTask(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusDisabled, stored.Status)
	require.Equal(t, "inst-1", stored.BoundInstanceID)
	count, err := db.CountMembers(ctx, testTaskID())
	require.NoError(t, err)
	require.Zero(t, count)

	require.NoError(t, svc.DeleteBoundTask(ctx, testTaskID(), "inst-1"))
	_, err = db.GetTask(ctx, testTaskID())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// 数据集已在 Storage 删除、任务记录还在（删除数据集之后进程退出）：重试仍然成功。
func TestDeleteBoundTaskAfterDatasetAlreadyDeleted(t *testing.T) {
	ctx := context.Background()
	svc, db, meta := boundTaskWithEnabledMember(t)
	task, err := db.GetTask(ctx, testTaskID())
	require.NoError(t, err)
	require.NoError(t, meta.DeleteDataset(ctx, task.SpaceID, task.ResultDatasetID))

	require.NoError(t, svc.DeleteBoundTask(ctx, testTaskID(), "inst-1"))
	_, err = db.GetTask(ctx, testTaskID())
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// 关联的是别的实例：拒绝，任务、成员与数据集都不动。
func TestDeleteBoundTaskRejectsOtherInstance(t *testing.T) {
	ctx := context.Background()
	svc, db, meta := boundTaskWithEnabledMember(t)

	err := svc.DeleteBoundTask(ctx, testTaskID(), "inst-2")
	require.ErrorIs(t, err, store.ErrConflict)
	require.ErrorContains(t, err, "inst-1")
	stored, err := db.GetTask(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusEnabled, stored.Status)
	count, err := db.CountMembers(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NotContains(t, meta.writeOps, "delete")
}

// 还没有关联任何实例（实例从未启用）：也按“关联的不是这个实例”拒绝。
func TestDeleteBoundTaskRejectsUnboundTask(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)

	err := svc.DeleteBoundTask(ctx, testTaskID(), "inst-1")
	require.ErrorIs(t, err, store.ErrConflict)
	require.ErrorContains(t, err, "还没有关联")
	stored, err := db.GetTask(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusEnabled, stored.Status)
}

// 有补算在排队或执行：拒绝，任务、成员与数据集都不动；补算结束后再删能做完。
func TestDeleteBoundTaskRejectsActiveRecalc(t *testing.T) {
	ctx := context.Background()
	svc, db, meta := boundTaskWithEnabledMember(t)
	job, err := db.CreateRecalcJob(ctx, store.RecalcJob{RequestID: "req-1", TaskID: testTaskID(), StartTime: 1, EndTime: 100, Status: store.RecalcStatusAccepted})
	require.NoError(t, err)

	err = svc.DeleteBoundTask(ctx, testTaskID(), "inst-1")
	require.ErrorIs(t, err, store.ErrConflict)
	require.ErrorContains(t, err, "请先取消补算")
	require.ErrorContains(t, err, job.JobID)
	stored, err := db.GetTask(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusEnabled, stored.Status, "拒绝时不停用任务")
	count, err := db.CountMembers(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NotContains(t, meta.writeOps, "delete")

	_, err = db.UpdateRecalcJob(ctx, job.JobID, func(current *store.RecalcJob) error {
		current.Status = store.RecalcStatusCancelled
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, svc.DeleteBoundTask(ctx, testTaskID(), "inst-1"))
}

// 结果数据集被其他 View 引用：在停用任务、删成员之前按冲突拒绝；只检查（CheckBoundTaskDeletable）也给出同样的结果。
func TestDeleteBoundTaskRejectsReferencedDatasetBeforeChanging(t *testing.T) {
	ctx := context.Background()
	svc, db, meta := boundTaskWithEnabledMember(t)
	meta.deletableErr = fmt.Errorf("%w：结果数据集 x 仍被 View view_other 引用", storageio.ErrDatasetReferenced)

	err := svc.CheckBoundTaskDeletable(ctx, testTaskID(), "inst-1")
	require.ErrorIs(t, err, store.ErrConflict)
	err = svc.DeleteBoundTask(ctx, testTaskID(), "inst-1")
	require.ErrorIs(t, err, store.ErrConflict)
	require.ErrorContains(t, err, "view_other")
	stored, err := db.GetTask(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusEnabled, stored.Status)
	count, err := db.CountMembers(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// 读不到 Storage：按错误返回（不是冲突），同样什么也不改。
	meta.deletableErr = errors.New("storage unavailable")
	err = svc.DeleteBoundTask(ctx, testTaskID(), "inst-1")
	require.Error(t, err)
	require.NotErrorIs(t, err, store.ErrConflict)

	meta.deletableErr = nil
	require.NoError(t, svc.CheckBoundTaskDeletable(ctx, testTaskID(), "inst-1"))
	require.NoError(t, svc.DeleteBoundTask(ctx, testTaskID(), "inst-1"))
	require.NoError(t, svc.CheckBoundTaskDeletable(ctx, testTaskID(), "inst-1"), "任务不存在算可以删")
}
