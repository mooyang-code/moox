package delist

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/rpc"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	pb "github.com/mooyang-code/moox/modules/callbackdispatcher/proto/callbackdispatchergen"
	"github.com/stretchr/testify/require"
)

func (h *harness) service() *rpc.Service {
	registry := action.NewRegistry()
	registry.Register(store.SystemActionSellDelistedHoldings, h.handler)
	return &rpc.Service{Store: h.store, Actions: registry, Now: h.clock.Now}
}

// 注册处理器之后，目录里列出这个系统动作。
func TestCatalogListsRegisteredSystemAction(t *testing.T) {
	h := newHarness(t)
	rsp, err := h.service().ListSubscribableEvents(context.Background(), &pb.ListSubscribableEventsReq{})
	require.NoError(t, err)
	require.Equal(t, []pb.SystemAction{pb.SystemAction_SYSTEM_ACTION_SELL_DELISTED_HOLDINGS}, rsp.AvailableSystemActions)
}

// 取消并解除禁止：善后成功后清掉“善后未完成”；Trade 不可达时标记保留，再取消一次（重试善后）才做成。
func TestCancelThroughServiceUnblocksAndRetriesCleanup(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	h.step("e1", baseTime)
	service := h.service()

	h.trade.unblockHook = func(context.Context) error { return errors.New("Trade 不可达") }
	rsp, err := service.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "e1"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
	got := h.execution("e1")
	require.Equal(t, store.StatusCanceled, got.Status)
	require.True(t, got.CleanupPending)
	require.Contains(t, got.LastError, "解除禁止买入失败")
	require.True(t, h.trade.isBlocked())

	h.trade.unblockHook = nil
	rsp, err = service.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "e1"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.False(t, rsp.Execution.CleanupPending)
	require.False(t, h.trade.isBlocked())
}

// 真实下架取消时同标的演练没结束：照样解除禁止，演练下一次推进时自己停下。
func TestCancelRealWithDrillThroughService(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	delistAt := baseTime.Add(48 * time.Hour)
	h.addExecution("real", delistAt, "ALL")
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("real", baseTime)
	h.step("drill", baseTime)
	service := h.service()

	rsp, err := service.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "real"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.False(t, h.execution("real").CleanupPending)
	require.False(t, h.trade.isBlocked())
	require.Equal(t, store.StatusCanceled, h.step("drill", baseTime.Add(time.Minute)).Status)
}

// 详情接口返回各轮步骤。
func TestDetailListsDelistSteps(t *testing.T) {
	h := newHarness(t)
	h.trade.setHolders(live("crypto", "la-1"))
	delistAt := baseTime.Add(48 * time.Hour)
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)
	h.step("e1", delistAt.Add(-24*time.Hour))
	rsp, err := h.service().GetEventExecution(context.Background(), &pb.GetEventExecutionReq{ExecutionId: "e1"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	var rounds []uint32
	for _, step := range rsp.Detail.DelistSteps {
		rounds = append(rounds, step.Round)
	}
	require.Equal(t, []uint32{0, 1, 1}, rounds, "禁止、汇总、一个组合")
}
