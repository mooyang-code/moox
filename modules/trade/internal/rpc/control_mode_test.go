package rpc

import (
	"context"
	"testing"

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
			rsp, err := h.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
				Name:            "account",
				Environment:     tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER,
				SettlementAsset: "USDT",
				ControlMode:     mode,
			})
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
			require.Equal(t, tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER, rsp.GetLogicalAccount().GetEnvironment())
			if want == tradepb.ControlMode_CONTROL_MODE_MANUAL {
				require.Empty(t, rsp.GetLogicalAccount().GetAutomationState())
				require.Empty(t, rsp.GetLogicalAccount().GetPauseReason())
			} else {
				require.Equal(t, "PAUSED", rsp.GetLogicalAccount().GetAutomationState())
			}
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
