package input

import (
	"context"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/spacecontext"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
)

type factorTasksFake struct {
	FactorClient
	reqs  []*factorpb.ListFactorTasksReq
	pages [][]*factorpb.FactorTaskInfo
}

func (f *factorTasksFake) ListFactorTasks(_ context.Context, req *factorpb.ListFactorTasksReq, _ ...client.Option) (*factorpb.ListFactorTasksRsp, error) {
	f.reqs = append(f.reqs, req)
	page := int(req.GetPage().GetPage())
	if page <= 0 || page > len(f.pages) {
		return &factorpb.ListFactorTasksRsp{
			RetInfo:    &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			PageResult: &commonpb.PageResult{HasMore: false},
		}, nil
	}
	return &factorpb.ListFactorTasksRsp{
		RetInfo:     &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		FactorTasks: f.pages[page-1],
		PageResult:  &commonpb.PageResult{HasMore: page < len(f.pages)},
	}, nil
}

func TestListFactorTaskIDsForwardsSpaceIDAndPaginates(t *testing.T) {
	fake := &factorTasksFake{
		pages: [][]*factorpb.FactorTaskInfo{
			{{FactorTask: &factorpb.FactorTask{TaskId: "ftask_a"}}},
			{{FactorTask: &factorpb.FactorTask{TaskId: "ftask_b"}}},
		},
	}
	rpcClient := &RPCClient{Factor: fake}

	ctx := spacecontext.WithSpaceID(context.Background(), "crypto")
	ids, err := rpcClient.ListFactorTaskIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"ftask_a": true, "ftask_b": true}, ids)
	require.Len(t, fake.reqs, 2)
	require.Equal(t, "crypto", fake.reqs[0].GetSpaceId())
	require.Equal(t, uint32(1), fake.reqs[0].GetPage().GetPage())
	require.Equal(t, "crypto", fake.reqs[1].GetSpaceId())
	require.Equal(t, uint32(2), fake.reqs[1].GetPage().GetPage())

	// 从网关注入的 X-Space-Id 元数据同样能提取 space_id。
	fake.reqs = nil
	metaCtx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerMetaData(codec.MetaData{"X-Space-Id": []byte("stockcn")})
	_, err = rpcClient.ListFactorTaskIDs(metaCtx)
	require.NoError(t, err)
	require.Len(t, fake.reqs, 2)
	require.Equal(t, "stockcn", fake.reqs[0].GetSpaceId())

	// 未带空间上下文时不设 space_id（返回全量）。
	fake.reqs = nil
	_, err = rpcClient.ListFactorTaskIDs(context.Background())
	require.NoError(t, err)
	require.Len(t, fake.reqs, 2)
	require.Empty(t, fake.reqs[0].GetSpaceId())
}
