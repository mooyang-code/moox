package fetchplan

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
)

// 标的改了交易所符号（标的集合不变）：对账器给数据集选生效周期 P、不重新登记快照，在计划建立的同一个事务里
// 写下从 P 起的新符号；时间线上 P 之前仍是旧符号，补采按它取。
func TestSymbolChangeWritesATimelineRowFromP(t *testing.T) {
	h := newPlanHarness(t, 3)
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	h.store.shadow = db.FetchPlans()

	renamed := false
	r := h.reconciler()
	r.Groups.ResolveSymbol = func(_, _, _, subjectID string) (string, error) {
		if renamed && subjectID == "S001-USDT" {
			return "S001NEW", nil
		}
		return strings.ReplaceAll(subjectID, "-", ""), nil
	}
	planBaselineSettle(t, h, r)
	registered := h.registrar.calls

	renamed = true
	r.lastVerified = h.now
	require.NoError(t, h.reconcile(r))
	open := h.open()
	require.NotNil(t, open, "只改符号也生成新计划")
	change := h.content(open).Datasets["ds_spot|1m"]
	require.True(t, change.SymbolsChanged)
	require.False(t, change.HashChanged)
	require.NotZero(t, change.P)
	require.Equal(t, "S001NEW", change.Symbols["S001-USDT"])

	// 计划一建立时间线就有这一行（与计划记录同一个事务），不等登记。
	entries, err := db.SeriesTimeline().ListBySpace(context.Background(), "crypto")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	timeline := klinecheck.BuildTimeline(entries)
	p := time.Unix(change.P, 0).UTC()
	symbol, ok := timeline.SymbolAt("ds_spot|1m", "S001-USDT", p.Add(-time.Minute))
	require.True(t, ok)
	require.Equal(t, "S001USDT", symbol, "P 之前是旧符号")
	symbol, ok = timeline.SymbolAt("ds_spot|1m", "S001-USDT", p)
	require.True(t, ok)
	require.Equal(t, "S001NEW", symbol, "P 起是新符号")
	symbol, _ = timeline.SymbolAt("ds_spot|1m", "S001-USDT", p.Add(-24*time.Hour))
	require.Equal(t, "S001USDT", symbol, "早于最早一行沿用最早一行的符号")

	planBaselineSettle(t, h, r)
	require.Equal(t, registered, h.registrar.calls, "符号变化不重新登记快照")
}
