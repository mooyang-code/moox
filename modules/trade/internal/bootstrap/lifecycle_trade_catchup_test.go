package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

type accountsFake struct {
	list []store.TradingAccountRecord
	err  error
}

func (f accountsFake) ListAllTradingAccounts(context.Context) ([]store.TradingAccountRecord, error) {
	return f.list, f.err
}

type quiesceFake struct{ err error }

func (f quiesceFake) Quiesce(context.Context) error { return f.err }

// Trade 的追赶：每个启用的实盘账户与交易所的最近一次对账是新鲜的、已就绪、无错误、余额已确认，并且没有目标在收敛。
func TestTradeCatchupRequiresFreshReconciledLiveAccounts(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second).UnixMilli()
	stale := now.Add(-10 * time.Minute).UnixMilli()
	live := func(id string, syncAt int64, mutate func(*store.TradingAccountRecord)) store.TradingAccountRecord {
		record := store.TradingAccountRecord{TradingAccountID: id, ExecutionMode: "LIVE", Status: "ENABLED", Ready: true, LastSyncAt: syncAt}
		if mutate != nil {
			mutate(&record)
		}
		return record
	}
	paper := store.TradingAccountRecord{TradingAccountID: "paper", ExecutionMode: "PAPER", Status: "ENABLED"}
	disabled := live("off", stale, func(r *store.TradingAccountRecord) { r.Status = "DISABLED" })
	hook := func(accounts accountsFake, q quiesceFake) (lifecycle.Catchup, error) {
		return tradeCatchup(accounts, q, func() time.Time { return now })(context.Background(), lifecycle.Ref{}, now.Add(-time.Hour))
	}

	got, err := hook(accountsFake{list: []store.TradingAccountRecord{paper, disabled, live("a", fresh, nil), live("b", fresh, nil)}}, quiesceFake{})
	require.NoError(t, err)
	require.True(t, got.CaughtUp, "纸面与停用账户不参与对账判定")
	require.Equal(t, int64(2), got.Total)
	require.Equal(t, int64(2), got.Done)

	for name, account := range map[string]store.TradingAccountRecord{
		"对账陈旧":  live("c", stale, nil),
		"账户未就绪": live("c", fresh, func(r *store.TradingAccountRecord) { r.Ready = false }),
		"有错误":   live("c", fresh, func(r *store.TradingAccountRecord) { r.LastError = "签名失败" }),
		"余额待确认": live("c", fresh, func(r *store.TradingAccountRecord) { r.BalancePendingUntil = now.UnixMilli() }),
	} {
		got, err := hook(accountsFake{list: []store.TradingAccountRecord{live("a", fresh, nil), account}}, quiesceFake{})
		require.NoError(t, err, name)
		require.False(t, got.CaughtUp, name)
		require.Equal(t, "交易所对账", got.BlockedBy, name)
		require.Equal(t, int64(1), got.Done, name)
		require.Contains(t, got.Message, "账户 c", name)
	}

	got, _ = hook(accountsFake{list: []store.TradingAccountRecord{live("a", fresh, nil)}}, quiesceFake{err: errors.New("1 个目标在途")})
	require.False(t, got.CaughtUp)
	require.Contains(t, got.Message, "仍有目标在收敛")

	_, err = hook(accountsFake{err: errors.New("数据库不可用")}, quiesceFake{})
	require.Error(t, err)

	object, err := tradeCatchup(accountsFake{}, quiesceFake{}, time.Now)(context.Background(), lifecycle.Ref{Kind: "x", ID: "y"}, now)
	require.NoError(t, err)
	require.True(t, object.CaughtUp)
}
