package runtime

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 账户类型变化（例如检测结果更新）会改变钱包对应和适配器的接口，会话必须重建。
func TestSessionConfigChangesWhenAccountModeChanges(t *testing.T) {
	left := store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: "account-1", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"},
		ExecutionMode: "LIVE", AccountMode: "BINANCE_STANDARD", Status: "ENABLED",
	}
	right := left
	require.True(t, sameSessionConfig(left, right))
	right.AccountMode = "BINANCE_PORTFOLIO_MARGIN"
	require.False(t, sameSessionConfig(left, right))
}
