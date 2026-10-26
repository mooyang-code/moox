package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

func spaceCtx(space string) context.Context {
	ctx, message := codec.WithNewMessage(context.Background())
	if space != "" {
		message.WithServerMetaData(codec.MetaData{"X-Space-Id": []byte(space)})
	}
	return ctx
}

// 计算任务按全局 task_id 定位：一个空间的管理员不能读取、停用、删除或补算其他空间的任务；没有空间元数据的内部调用不受限。
func TestFactorTaskOperationsAreScopedToTheCallerSpace(t *testing.T) {
	catalog := &catalogFake{
		set:  domain.FactorTask{TaskID: "ftask_b", SpaceID: "stockcn"},
		sets: []domain.FactorTask{{TaskID: "ftask_a", SpaceID: "crypto"}, {TaskID: "ftask_b", SpaceID: "stockcn"}},
	}
	recalc := &recalcFake{job: RecalcJob{JobID: "job-1", TaskID: "ftask_b"}}
	svc := NewService(catalog, recalc)

	denied := commonpb.ErrorCode_INVALID_PARAM
	get, _ := svc.GetFactorTask(spaceCtx("crypto"), &factorpb.GetFactorTaskReq{TaskId: "ftask_b"})
	require.Equal(t, denied, get.GetRetInfo().GetCode(), "跨空间读取")
	status, _ := svc.SetFactorTaskStatus(spaceCtx("crypto"), &factorpb.SetFactorTaskStatusReq{TaskId: "ftask_b", Status: domain.TaskStatusDisabled})
	require.Equal(t, denied, status.GetRetInfo().GetCode(), "跨空间停用")
	require.NotEqual(t, domain.TaskStatusDisabled, catalog.set.Status, "被拒绝的请求不能改动任务")
	del, _ := svc.DeleteFactorTask(spaceCtx("crypto"), &factorpb.DeleteFactorTaskReq{TaskId: "ftask_b"})
	require.Equal(t, denied, del.GetRetInfo().GetCode())
	job, _ := svc.GetRecalcJob(spaceCtx("crypto"), &factorpb.GetRecalcJobReq{JobId: "job-1"})
	require.Equal(t, denied, job.GetRetInfo().GetCode(), "补算作业通过 job → set → space 校验")
	cancel, _ := svc.CancelRecalcJob(spaceCtx("crypto"), &factorpb.CancelRecalcJobReq{JobId: "job-1"})
	require.Equal(t, denied, cancel.GetRetInfo().GetCode())

	list, _ := svc.ListFactorTasks(spaceCtx("crypto"), &factorpb.ListFactorTasksReq{})
	require.Len(t, list.GetFactorTasks(), 1, "列表只返回调用方所属空间的任务")
	require.Equal(t, "ftask_a", list.GetFactorTasks()[0].GetFactorTask().GetTaskId())

	own, _ := svc.GetFactorTask(spaceCtx("stockcn"), &factorpb.GetFactorTaskReq{TaskId: "ftask_b"})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, own.GetRetInfo().GetCode(), "本空间的任务可以访问")
	internal, _ := svc.GetFactorTask(spaceCtx(""), &factorpb.GetFactorTaskReq{TaskId: "ftask_b"})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, internal.GetRetInfo().GetCode(), "没有空间元数据的内部调用不受限")
	all, _ := svc.ListFactorTasks(spaceCtx(""), &factorpb.ListFactorTasksReq{})
	require.Len(t, all.GetFactorTasks(), 2)

	created, _ := svc.CreateFactorTask(spaceCtx("crypto"), &factorpb.CreateFactorTaskReq{FactorTask: &factorpb.FactorTask{TaskId: "ftask_new", SpaceId: "stockcn", SourceDatasetId: "d", Freq: "1m"}})
	require.Equal(t, denied, created.GetRetInfo().GetCode(), "不能在请求空间之外创建任务")
}

// 因子定义是全局的，但引用关系（任务 ID、启停状态）属于各自空间：其他空间的调用方不能通过 GetFactor/ListFactors 看到。
func TestFactorUsagesAreScopedToTheCallerSpace(t *testing.T) {
	catalog := &catalogFake{
		set: domain.FactorTask{TaskID: "ftask_b", SpaceID: "stockcn"},
		infos: []domain.FactorInfo{{
			Factor: domain.FactorDef{FactorID: "f1"},
			Usages: []domain.FactorUsage{{TaskID: "ftask_b", Status: domain.MemberStatusEnabled}},
		}},
	}
	svc := NewService(catalog, &recalcFake{})

	other, _ := svc.GetFactor(spaceCtx("crypto"), &factorpb.GetFactorReq{FactorId: "f1"})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, other.GetRetInfo().GetCode())
	require.Empty(t, other.GetUsages(), "其他空间的引用关系不能外泄")
	own, _ := svc.GetFactor(spaceCtx("stockcn"), &factorpb.GetFactorReq{FactorId: "f1"})
	require.Len(t, own.GetUsages(), 1)
	internal, _ := svc.GetFactor(spaceCtx(""), &factorpb.GetFactorReq{FactorId: "f1"})
	require.Len(t, internal.GetUsages(), 1, "内部调用看到全量引用")

	list, _ := svc.ListFactors(spaceCtx("crypto"), &factorpb.ListFactorsReq{})
	require.Len(t, list.GetFactors(), 1)
	require.Empty(t, list.GetFactors()[0].GetUsages())
	byForeignSet, _ := svc.ListFactors(spaceCtx("crypto"), &factorpb.ListFactorsReq{TaskId: "ftask_b"})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, byForeignSet.GetRetInfo().GetCode(), "不能按其他空间的任务查询成员")
}
