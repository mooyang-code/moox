package papersimulation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestCreatePreservesControlModeAndRejectsUnknownAtomically(t *testing.T) {
	for _, mode := range []string{"", "STRATEGY", "MANUAL", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			s := Service{Store: db}
			result, err := s.Create(context.Background(), CreateCommand{SpaceID: "space", AccountName: "paper", LogicalAccountName: "logical", ControlMode: mode, Exchange: "BINANCE", MarketType: "SPOT", SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("100")})
			if mode == "unknown" {
				require.Error(t, err)
				var count int64
				require.NoError(t, db.DBForTest().Table("t_trading_accounts").Count(&count).Error)
				require.Zero(t, count)
				return
			}
			require.NoError(t, err)
			want := mode
			if want == "" {
				want = "STRATEGY"
			}
			require.Equal(t, want, result.LogicalAccount.ControlMode)
			persisted, err := db.GetLogicalAccount(context.Background(), "space", result.LogicalAccount.LogicalAccountID)
			require.NoError(t, err)
			require.Equal(t, want, persisted.ControlMode)
		})
	}
}
