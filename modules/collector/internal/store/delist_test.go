package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

// 已到期的下架：按每个代码最新一行判断（改过下架时间时以最新的为准），只看给定的交易所、市场、范围。
func TestDelistedSymbolsUsesLatestNoticePerSymbol(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	repo := db.DelistNotices()
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	n := 0
	register := func(market, symbol, scope string, at time.Time) {
		n++
		id := "evt-" + time.Duration(n).String()
		_, _, err := repo.Register(ctx, DelistNotice{Exchange: "binance", Market: market, Symbol: symbol, Scope: scope, DelistAt: at, Source: "test", SeenAt: now},
			func(int) (OutboxEvent, error) {
				return OutboxEvent{EventID: id, SpaceID: "crypto", EventName: "e", SubjectToken: symbol, Payload: []byte("{}")}, nil
			})
		require.NoError(t, err)
	}
	register("SPOT", "GONEUSDT", "ALL", now.Add(-time.Hour))
	register("SPOT", "LATERUSDT", "ALL", now.Add(time.Hour))
	register("SPOT", "MOVEDUSDT", "ALL", now.Add(-2*time.Hour))
	register("SPOT", "MOVEDUSDT", "ALL", now.Add(24*time.Hour)) // 推迟了：以最新一行为准
	register("SPOT", "DRILLUSDT", "PAPER_ONLY", now.Add(-time.Hour))
	register("SWAP", "SWAPUSDT", "ALL", now.Add(-time.Hour))

	got, err := repo.DelistedSymbols(ctx, "binance", "SPOT", "ALL", now)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"GONEUSDT": true}, got)
}
