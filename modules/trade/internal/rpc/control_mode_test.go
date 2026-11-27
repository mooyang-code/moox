package rpc

import (
	"context"
	"testing"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
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
			rsp, err := h.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{Name: "account", ExecutionMode: tradepb.ExecutionMode_EXECUTION_MODE_PAPER, SettlementAsset: "USDT", ControlMode: mode})
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

func TestPaperAccountControlModeRPC(t *testing.T) {
	for _, mode := range []tradepb.ControlMode{0, 1, 2, 99} {
		t.Run(mode.String(), func(t *testing.T) {
			s := openRPCStore(t)
			logicalSrv := &LogicalAccountServer{Store: s, LogicalAccounts: &logicalapp.Service{Store: s}}
			h := &AccountServer{
				Store:    s,
				Logical:  logicalSrv,
				Accounts: &accountapp.Service{Store: accountapp.Repository{Store: s}},
			}
			ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
			rsp, err := h.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
				Name: "paper", LogicalAccountName: "logical", Exchange: "binance",
				Markets: []string{"spot"}, SettlementAsset: "USDT", ControlMode: mode,
				Paper: &tradepb.PaperConfig{InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0"},
			})
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
