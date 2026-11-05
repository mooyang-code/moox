package accountsync

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func standardMixedAccount(pending map[string]int64) store.TradingAccountRecord {
	return store.TradingAccountRecord{
		Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "LIVE",
		AccountMode: string(exchange.AccountModeBinanceStandard), BalancePending: pending,
	}
}

// 合约成交只让合约钱包等确认：快照读取时间追上就清掉，不要求现货钱包的余额内容更新时间追上合约成交。
func TestSwapFillOnlyHoldsFuturesWallet(t *testing.T) {
	account := standardMixedAccount(map[string]int64{"FUTURES": 3_000})
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: time.UnixMilli(1_000), // 现货钱包的余额很久没变
		WalletUpdatedAt:   map[exchange.Wallet]time.Time{exchange.WalletSpot: time.UnixMilli(1_000), exchange.WalletFutures: time.UnixMilli(3_500)},
	}

	covered := coveredWallets(account, snapshot, 4_000, true)

	require.Equal(t, []exchange.Wallet{exchange.WalletFutures}, covered)
}

// 现货成交：现货钱包要求余额内容的更新时间也不早于水位，否则会按成交前的余额重复下单。
func TestSpotWalletNeedsFreshBalanceContent(t *testing.T) {
	account := standardMixedAccount(map[string]int64{"SPOT": 3_000})
	stale := exchange.AccountSnapshot{
		ExchangeUpdatedAt: time.UnixMilli(2_000),
		WalletUpdatedAt:   map[exchange.Wallet]time.Time{exchange.WalletSpot: time.UnixMilli(2_000)},
	}
	fresh := exchange.AccountSnapshot{
		ExchangeUpdatedAt: time.UnixMilli(2_000),
		WalletUpdatedAt:   map[exchange.Wallet]time.Time{exchange.WalletSpot: time.UnixMilli(3_000)},
	}

	require.Empty(t, coveredWallets(account, stale, 4_000, true), "现货余额内容比成交旧")
	require.Equal(t, []exchange.Wallet{exchange.WalletSpot}, coveredWallets(account, fresh, 4_000, true))
}

// 读取时间早于水位，或快照没有被采用时，什么都不清；两个钱包各自判断。
func TestCoveredWalletsEachWalletIndependent(t *testing.T) {
	account := standardMixedAccount(map[string]int64{"SPOT": 3_000, "FUTURES": 5_000})
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: time.UnixMilli(3_500),
		WalletUpdatedAt:   map[exchange.Wallet]time.Time{exchange.WalletSpot: time.UnixMilli(3_500), exchange.WalletFutures: time.UnixMilli(3_500)},
	}

	require.Equal(t, []exchange.Wallet{exchange.WalletSpot}, coveredWallets(account, snapshot, 4_000, true), "合约水位 5_000 还没被 4_000 的读取覆盖")
	require.Empty(t, coveredWallets(account, snapshot, 4_000, false), "快照没被采用")
}

// OKX 两个市场共用统一钱包，没有按钱包记录更新时间时按整体更新时间判断。
func TestOKXUnifiedWalletFallsBackToSnapshotTime(t *testing.T) {
	account := store.TradingAccountRecord{
		Exchange: "OKX", Markets: []string{"SPOT", "SWAP"}, ExecutionMode: "LIVE",
		BalancePending: map[string]int64{"UNIFIED": 3_000},
	}
	snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: time.UnixMilli(3_200)}

	require.Equal(t, []exchange.Wallet{exchange.WalletUnified}, coveredWallets(account, snapshot, 4_000, true))
}

// 私有账户更新涉及哪些钱包：带了余额明细按明细，没带就是账户所有市场的钱包。
func TestPrivateUpdateWallets(t *testing.T) {
	account := standardMixedAccount(nil)

	withBalances := exchange.AccountSnapshot{Balances: []exchange.AssetBalance{{Wallet: exchange.WalletFutures, Asset: "USDT"}}}
	require.Equal(t, []exchange.Wallet{exchange.WalletFutures}, privateUpdateWallets(account, withBalances))
	require.ElementsMatch(t, []exchange.Wallet{exchange.WalletSpot, exchange.WalletFutures}, privateUpdateWallets(account, exchange.AccountSnapshot{}))
}
