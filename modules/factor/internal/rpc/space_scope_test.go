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

// 计算任务按全局 set_id 定位：一个空间的管理员不能读取、停用、删除或补算其他空间的任务；没有空间元数据的内部调用不受限。
func TestFactorSetOperationsAreScopedToTheCallerSpace(t *testing.T) {
	catalog := &catalogFake{
		set:  domain.FactorSet{SetID: "fset_b", SpaceID: "stockcn"},
		sets: []domain.FactorSet{{SetID: "fset_a", SpaceID: "crypto"}, {SetID: "fset_b", SpaceID: "stockcn"}},
	}
	recalc := &recalcFake{job: RecalcJob{JobID: "job-1", SetID: "fset_b"}}
	svc := NewService(catalog, recalc)

	denied := commonpb.ErrorCode_INVALID_PARAM
	get, _ := svc.GetFactorSet(spaceCtx("crypto"), &factorpb.GetFactorSetReq{SetId: "fset_b"})
	require.Equal(t, denied, get.GetRetInfo().GetCode(), "跨空间读取")
	status, _ := svc.SetFactorSetStatus(spaceCtx("crypto"), &factorpb.SetFactorSetStatusReq{SetId: "fset_b", Status: domain.SetStatusDisabled})
	require.Equal(t, denied, status.GetRetInfo().GetCode(), "跨空间停用")
	require.NotEqual(t, domain.SetStatusDisabled, catalog.set.Status, "被拒绝的请求不能改动任务")
	del, _ := svc.DeleteFactorSet(spaceCtx("crypto"), &factorpb.DeleteFactorSetReq{SetId: "fset_b"})
	require.Equal(t, denied, del.GetRetInfo().GetCode())
	job, _ := svc.GetRecalcJob(spaceCtx("crypto"), &factorpb.GetRecalcJobReq{JobId: "job-1"})
	require.Equal(t, denied, job.GetRetInfo().GetCode(), "补算任务通过 job → set → space 校验")
	cancel, _ := svc.CancelRecalcJob(spaceCtx("crypto"), &factorpb.CancelRecalcJobReq{JobId: "job-1"})
	require.Equal(t, denied, cancel.GetRetInfo().GetCode())

	list, _ := svc.ListFactorSets(spaceCtx("crypto"), &factorpb.ListFactorSetsReq{})
	require.Len(t, list.GetFactorSets(), 1, "列表只返回调用方所属空间的任务")
	require.Equal(t, "fset_a", list.GetFactorSets()[0].GetFactorSet().GetSetId())

	own, _ := svc.GetFactorSet(spaceCtx("stockcn"), &factorpb.GetFactorSetReq{SetId: "fset_b"})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, own.GetRetInfo().GetCode(), "本空间的任务可以访问")
	internal, _ := svc.GetFactorSet(spaceCtx(""), &factorpb.GetFactorSetReq{SetId: "fset_b"})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, internal.GetRetInfo().GetCode(), "没有空间元数据的内部调用不受限")
	all, _ := svc.ListFactorSets(spaceCtx(""), &factorpb.ListFactorSetsReq{})
	require.Len(t, all.GetFactorSets(), 2)

	created, _ := svc.CreateFactorSet(spaceCtx("crypto"), &factorpb.CreateFactorSetReq{FactorSet: &factorpb.FactorSet{SetId: "fset_new", SpaceId: "stockcn", SourceDatasetId: "d", Freq: "1m"}})
	require.Equal(t, denied, created.GetRetInfo().GetCode(), "不能在请求空间之外创建任务")
}
