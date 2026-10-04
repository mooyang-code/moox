package rpc

import (
	"context"
	"testing"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func TestLogicalAccountControlModeRPC(t *testing.T) {
	for _, mode := range []tradepb.ControlMode{0, 1, 2, 99} {
		t.Run(mode.String(), func(t *testing.T) {
			s := openRPCStore(t)
			h := &LogicalAccountServer{LogicalAccounts: &logicalapp.Service{Store: s}, Store: s, NewID: func() string { return "logical-1" }}
			ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
			rsp, err := h.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{Name: "account", ExecutionMode: tradepb.ExecutionMode_EXECUTION_MODE_PAPER, MarketType: tradepb.MarketType_MARKET_TYPE_SPOT, SettlementAsset: "USDT", ControlMode: mode})
			require.NoError(t, err)
			if mode == 99 {
				require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
				rows, err := s.ListLogicalAccounts(ctx, "space-1")
				require.NoError(t, err)
				require.Empty(t, rows)
				return
			}
			require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo())
			want := mode
			if want == 0 {
				want = tradepb.ControlMode_CONTROL_MODE_STRATEGY
			}
			require.Equal(t, want, rsp.GetLogicalAccount().GetControlMode())
			stored, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, controlModeFromPB(want), stored.ControlMode)
			queried, err := h.GetLogicalAccount(ctx, &tradepb.GetLogicalAccountReq{LogicalAccountId: "logical-1"})
			require.NoError(t, err)
			require.Equal(t, want, queried.GetLogicalAccount().GetControlMode())
		})
	}
	require.Equal(t, tradepb.ControlMode_CONTROL_MODE_UNSPECIFIED, controlModeToPB("unknown"))
}

func TestPaperSimulationControlModeRPC(t *testing.T) {
	for _, mode := range []tradepb.ControlMode{0, 1, 2, 99} {
		t.Run(mode.String(), func(t *testing.T) {
			s := openRPCStore(t)
			h := &ConsoleServer{Store: s, LogicalAccountServer: &LogicalAccountServer{Store: s, LogicalAccounts: &logicalapp.Service{Store: s}}, Paper: &papersimulation.Service{Store: s}}
			ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
			rsp, err := h.CreatePaperSimulation(ctx, &tradepb.CreatePaperSimulationReq{AccountName: "paper", LogicalAccountName: "logical", Exchange: tradepb.Exchange_EXCHANGE_BINANCE, MarketType: tradepb.MarketType_MARKET_TYPE_SPOT, SettlementAsset: "USDT", InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0", ControlMode: mode})
			require.NoError(t, err)
			if mode == 99 {
				require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
				var count int64
				require.NoError(t, s.DBForTest().Table("t_trading_accounts").Count(&count).Error)
				require.Zero(t, count)
				return
			}
			require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo())
			want := mode
			if want == 0 {
				want = tradepb.ControlMode_CONTROL_MODE_STRATEGY
			}
			require.Equal(t, want, rsp.GetLogicalAccount().GetControlMode())
			stored, err := s.GetLogicalAccount(ctx, "space-1", rsp.GetLogicalAccount().GetLogicalAccountId())
			require.NoError(t, err)
			require.Equal(t, controlModeFromPB(want), stored.ControlMode)
		})
	}
}

func TestManualControlModeClaimConflictIsConsistent(t *testing.T) {
	db := openRPCStore(t)
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	h := &LogicalAccountServer{Store: db, LogicalAccounts: &logicalapp.Service{Store: db}, NewID: func() string { return "manual-1" }}
	created, err := h.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{Name: "manual", ExecutionMode: tradepb.ExecutionMode_EXECUTION_MODE_PAPER, MarketType: tradepb.MarketType_MARKET_TYPE_SPOT, SettlementAsset: "USDT", ControlMode: tradepb.ControlMode_CONTROL_MODE_MANUAL})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode())
	before, err := db.GetLogicalAccount(ctx, "space-1", "manual-1")
	require.NoError(t, err)
	for _, req := range []*tradepb.ClaimLogicalAccountOwnerReq{
		{LogicalAccountId: "manual-1", RunnerId: "runner-1"},
		{LogicalAccountId: "manual-1", InstanceId: "instance-1", SessionId: "session-1", ExpectedAuthFence: before.AuthFence},
	} {
		rsp, err := h.ClaimLogicalAccountOwner(ctx, req)
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode(), req)
	}
	for _, req := range []*tradepb.RebindLogicalAccountOwnerReq{
		{LogicalAccountId: "manual-1", RunnerId: "runner-2", RebindKey: "request-1"},
		{LogicalAccountId: "manual-1", InstanceId: "instance-1", SessionId: "session-1", NewInstanceId: "instance-2", NewSessionId: "session-2", ExpectedAuthFence: "fence-1"},
	} {
		rsp, err := h.RebindLogicalAccountOwner(ctx, req)
		require.NoError(t, err)
		require.Equal(t, tradepb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode(), req)
	}
	after, err := db.GetLogicalAccount(ctx, "space-1", "manual-1")
	require.NoError(t, err)
	require.Equal(t, before, after)
}
