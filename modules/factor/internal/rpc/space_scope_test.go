package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
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
	recalc := &recalcFake{job: store.RecalcJob{JobID: "job-1", TaskID: "ftask_b"}}
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

// ListFactorTasks 支持可选 space_id 过滤，并与请求头 X-Space-Id 协同校验防止跨空间扫描。
func TestListFactorTasksSpaceFilterAndHeaderValidation(t *testing.T) {
	catalog := &catalogFake{
		set: domain.FactorTask{TaskID: "ftask_a", SpaceID: "crypto", Status: domain.TaskStatusEnabled},
		sets: []domain.FactorTask{
			{TaskID: "ftask_a", SpaceID: "crypto", Status: domain.TaskStatusEnabled},
			{TaskID: "ftask_b", SpaceID: "stockcn", Status: domain.TaskStatusEnabled},
			{TaskID: "ftask_c", SpaceID: "crypto", Status: domain.TaskStatusDisabled},
		},
	}
	svc := NewService(catalog, &recalcFake{})

	// 无空间头、无过滤参数：返回全量。
	all, err := svc.ListFactorTasks(spaceCtx(""), &factorpb.ListFactorTasksReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, all.GetRetInfo().GetCode())
	require.Equal(t, "", catalog.listSpaceID)
	require.Len(t, all.GetFactorTasks(), 3)

	// 无空间头、显式指定 space_id：只查该空间，避免跨空间扫描。
	onlyCrypto, err := svc.ListFactorTasks(spaceCtx(""), &factorpb.ListFactorTasksReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, onlyCrypto.GetRetInfo().GetCode())
	require.Equal(t, "crypto", catalog.listSpaceID)
	require.Len(t, onlyCrypto.GetFactorTasks(), 2)
	for _, info := range onlyCrypto.GetFactorTasks() {
		require.Equal(t, "crypto", info.GetFactorTask().GetSpaceId())
	}

	// 无空间头、同时按 space_id 与 status 过滤。
	enabledCrypto, err := svc.ListFactorTasks(spaceCtx(""), &factorpb.ListFactorTasksReq{SpaceId: "crypto", Status: domain.TaskStatusEnabled})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, enabledCrypto.GetRetInfo().GetCode())
	require.Len(t, enabledCrypto.GetFactorTasks(), 1)
	require.Equal(t, "ftask_a", enabledCrypto.GetFactorTasks()[0].GetFactorTask().GetTaskId())

	// 带 X-Space-Id 头且未传 space_id：自动下推到 store 按请求头空间查询。
	catalog.listSpaceID = ""
	scoped, err := svc.ListFactorTasks(spaceCtx("stockcn"), &factorpb.ListFactorTasksReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, scoped.GetRetInfo().GetCode())
	require.Equal(t, "stockcn", catalog.listSpaceID)
	require.Len(t, scoped.GetFactorTasks(), 1)
	require.Equal(t, "ftask_b", scoped.GetFactorTasks()[0].GetFactorTask().GetTaskId())

	// 带 X-Space-Id 头且 space_id 一致：放行。
	matching, err := svc.ListFactorTasks(spaceCtx("stockcn"), &factorpb.ListFactorTasksReq{SpaceId: "stockcn"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, matching.GetRetInfo().GetCode())
	require.Len(t, matching.GetFactorTasks(), 1)

	// 带 X-Space-Id 头且 space_id 指向其他空间：直接拒绝，不查库。
	catalog.listSpaceID = "sentinel"
	mismatch, err := svc.ListFactorTasks(spaceCtx("crypto"), &factorpb.ListFactorTasksReq{SpaceId: "stockcn"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, mismatch.GetRetInfo().GetCode())
	require.Equal(t, "sentinel", catalog.listSpaceID, "跨空间过滤在查库前被拒绝")
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

	// 其他空间的引用只给数量：管理台据此挡住删除和编辑
	require.Equal(t, uint32(1), other.GetOtherSpaceUsageCount())
	require.Equal(t, uint32(1), other.GetOtherSpaceEnabledCount())
	require.Zero(t, own.GetOtherSpaceUsageCount())
	require.Zero(t, internal.GetOtherSpaceUsageCount(), "内部调用看到全量引用，不再另算其他空间")

	list, _ := svc.ListFactors(spaceCtx("crypto"), &factorpb.ListFactorsReq{})
	require.Len(t, list.GetFactors(), 1)
	require.Empty(t, list.GetFactors()[0].GetUsages())
	require.Equal(t, uint32(1), list.GetFactors()[0].GetOtherSpaceEnabledCount())
	byForeignSet, _ := svc.ListFactors(spaceCtx("crypto"), &factorpb.ListFactorsReq{TaskId: "ftask_b"})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, byForeignSet.GetRetInfo().GetCode(), "不能按其他空间的任务查询成员")
}

// 其他空间停用着的引用只计入总数，不计入启用数：停用的引用不挡编辑。
func TestOtherSpaceUsageCountsSeparateEnabled(t *testing.T) {
	catalog := &catalogFake{
		set: domain.FactorTask{TaskID: "ftask_b", SpaceID: "crypto"},
		infos: []domain.FactorInfo{{
			Factor: domain.FactorDef{FactorID: "f1"},
			Usages: []domain.FactorUsage{
				{TaskID: "ftask_b", MemberKey: "f1", Status: domain.MemberStatusEnabled},
				{TaskID: "ftask_b", MemberKey: "f1__n_5", Status: domain.MemberStatusDisabled},
			},
		}},
	}
	svc := NewService(catalog, &recalcFake{})
	rsp, _ := svc.GetFactor(spaceCtx("stockcn"), &factorpb.GetFactorReq{FactorId: "f1"})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Empty(t, rsp.GetUsages())
	require.Equal(t, uint32(2), rsp.GetOtherSpaceUsageCount())
	require.Equal(t, uint32(1), rsp.GetOtherSpaceEnabledCount())
}
