package rpc

import (
	"context"
	"errors"
	"testing"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

// 响应里的 lanes 原样转述执行器的结论，不在接口层再判断一遍。
func TestTargetLanesRelayExecutorStatuses(t *testing.T) {
	statuses := []targetapp.LaneStatus{
		{InstrumentID: "A", State: targetapp.LaneUnknown},
		{InstrumentID: "B", State: targetapp.LaneWillOrder},
		{InstrumentID: "C", State: targetapp.LaneInPlace},
		{InstrumentID: "D", State: targetapp.LaneBelowMinNotional},
		{InstrumentID: "E", State: targetapp.LaneMarketDeferred, Reason: "停牌"},
		{InstrumentID: "F", State: targetapp.LaneBelowLot},
		{InstrumentID: "G", State: targetapp.LaneBlocked, Reason: "没有启用的成员支持该标的"},
	}
	var asked store.LogicalAccountTargetRecord
	h := &ExecutionServer{Lanes: func(_ context.Context, record store.LogicalAccountTargetRecord) ([]targetapp.LaneStatus, error) {
		asked = record
		return statuses, nil
	}}
	record := store.LogicalAccountTargetRecord{TargetID: "target-1", LogicalAccountID: "logical-1", Status: targetapp.StatusConverging}

	lanes := h.targetLanes(context.Background(), record, nil)

	require.Equal(t, "target-1", asked.TargetID)
	require.Len(t, lanes, len(statuses))
	want := []tradepb.TargetLaneState{
		tradepb.TargetLaneState_TARGET_LANE_STATE_UNKNOWN,
		tradepb.TargetLaneState_TARGET_LANE_STATE_WILL_ORDER,
		tradepb.TargetLaneState_TARGET_LANE_STATE_IN_PLACE,
		tradepb.TargetLaneState_TARGET_LANE_STATE_BELOW_MIN_NOTIONAL,
		tradepb.TargetLaneState_TARGET_LANE_STATE_MARKET_DEFERRED,
		tradepb.TargetLaneState_TARGET_LANE_STATE_BELOW_LOT,
		tradepb.TargetLaneState_TARGET_LANE_STATE_BLOCKED,
	}
	for index, lane := range lanes {
		require.Equal(t, statuses[index].InstrumentID, lane.GetInstrumentId())
		require.Equal(t, want[index], lane.GetState())
		require.Equal(t, statuses[index].Reason, lane.GetReason())
	}
}

// 没有目标、目标已收敛、目标没读出来时不问执行器；执行器报错时不给 lanes，页面按未知显示。
func TestTargetLanesSkippedOrUnavailable(t *testing.T) {
	calls := 0
	h := &ExecutionServer{Lanes: func(context.Context, store.LogicalAccountTargetRecord) ([]targetapp.LaneStatus, error) {
		calls++
		return nil, errors.New("成员读不出来")
	}}
	active := store.LogicalAccountTargetRecord{TargetID: "target-1", Status: targetapp.StatusConverging}
	converged := store.LogicalAccountTargetRecord{TargetID: "target-1", Status: targetapp.StatusConverged}

	require.Nil(t, h.targetLanes(context.Background(), store.LogicalAccountTargetRecord{}, nil))
	require.Nil(t, h.targetLanes(context.Background(), converged, nil))
	require.Nil(t, h.targetLanes(context.Background(), active, errors.New("读目标失败")))
	require.Zero(t, calls)
	require.Nil(t, h.targetLanes(context.Background(), active, nil))
	require.Equal(t, 1, calls)
	require.Nil(t, (&ExecutionServer{}).targetLanes(context.Background(), active, nil))
}
