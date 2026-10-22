package gapledger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func gap(subject string, minute int, factors ...string) []pipeline.GapEntry {
	var out []pipeline.GapEntry
	for _, factor := range factors {
		out = append(out, pipeline.GapEntry{
			SetID: "fset", Subject: subject, Period: t0.Add(time.Duration(minute) * time.Minute), FactorID: factor, DefinitionHash: "h-" + factor,
			TriggerEventID: "evt",
		})
	}
	return out
}

func open(t *testing.T, dir string, c *clock, giveUp time.Duration) (*Ledger, []Entry) {
	t.Helper()
	ledger, expired, err := Open(dir, c.Now, giveUp)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger, expired
}

// 追加后重启回放得到同样的待恢复集合；终结后不再待恢复。
func TestLedgerReplayRebuildsPendingSet(t *testing.T) {
	dir, c := t.TempDir(), &clock{now: t0}
	ledger, _ := open(t, dir, c, 0)
	require.NoError(t, ledger.RecordGaps(context.Background(), append(gap("BTC", 0, "a", "b"), gap("ETH", 1, "a")...)))
	pending := ledger.Pending()
	require.Len(t, pending, 3)
	require.Equal(t, "BTC", pending[0].Subject)

	require.NoError(t, ledger.MarkRepaired([]Ref{pending[0].Ref()}))
	require.Equal(t, 2, ledger.PendingCount())
	require.NoError(t, ledger.Close())

	reopened, _ := open(t, dir, c, 0)
	got := reopened.Pending()
	require.Len(t, got, 2)
	require.Equal(t, "b", got[0].FactorID, "BTC 的因子 a 已 repaired，回放后不再待恢复")
	require.Equal(t, "evt", got[0].Trigger)
}

// 同键再次 skip：skipID 取最后一条，发现时间取最早；用旧 skipID 确认不会移除新的待办。
func TestLedgerReSkipChangesSkipIDAndStaleRepairedKeepsEntry(t *testing.T) {
	dir, c := t.TempDir(), &clock{now: t0}
	ledger, _ := open(t, dir, c, 0)
	require.NoError(t, ledger.RecordGaps(context.Background(), gap("BTC", 0, "a")))
	first := ledger.Pending()[0]

	c.now = t0.Add(2 * time.Minute)
	require.NoError(t, ledger.RecordGaps(context.Background(), gap("BTC", 0, "a")))
	second := ledger.Pending()[0]
	require.NotEqual(t, first.SkipID, second.SkipID, "每条 skip 行都带新的 skipID")
	require.Equal(t, first.FirstSeen, second.FirstSeen, "发现时间保留最早的")

	require.NoError(t, ledger.MarkRepaired([]Ref{first.Ref()}))
	require.Equal(t, 1, ledger.PendingCount(), "期间再次失效：用旧 skipID 的确认不能移除新的待办")
	require.NoError(t, ledger.MarkRepaired([]Ref{second.Ref()}))
	require.Zero(t, ledger.PendingCount())

	// 回放得到同样的结果：旧 skipID 的 repaired 行不会移除新条目。
	require.NoError(t, ledger.Close())
	again, _ := open(t, dir, c, 0)
	require.Zero(t, again.PendingCount())
}

// 终结后又出现的同键新 skip 是新条目；定义哈希不同的待办互不影响。
func TestLedgerKeysIncludeDefinitionHash(t *testing.T) {
	dir, c := t.TempDir(), &clock{now: t0}
	ledger, _ := open(t, dir, c, 0)
	old := gap("BTC", 0, "a")
	next := gap("BTC", 0, "a")
	next[0].DefinitionHash = "h-a-v2"
	require.NoError(t, ledger.RecordGaps(context.Background(), append(old, next...)))
	require.Equal(t, 2, ledger.PendingCount())
	var oldRef Ref
	for _, entry := range ledger.Pending() {
		if entry.Hash == "h-a" {
			oldRef = entry.Ref()
		}
	}
	require.NoError(t, ledger.MarkCancelled([]Ref{oldRef}, "definition_changed"))
	remaining := ledger.Pending()
	require.Len(t, remaining, 1)
	require.Equal(t, "h-a-v2", remaining[0].Hash, "取消旧定义的待办不会误伤新定义的")
}

// 崩溃留下的半行被忽略，新行不会接在它后面；停机期间超期的待恢复项启动时追加 gave_up。
func TestLedgerHandlesTruncatedLineAndExpiresOverdueOnOpen(t *testing.T) {
	dir, c := t.TempDir(), &clock{now: t0}
	ledger, _ := open(t, dir, c, 0)
	require.NoError(t, ledger.RecordGaps(context.Background(), gap("BTC", 0, "a")))
	require.NoError(t, ledger.Close())
	path := filepath.Join(dir, "factor-gap-202610.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	require.NoError(t, err)
	_, err = file.WriteString(`{"event":"skip","at":"2026-10-14T06:0`) // 半行
	require.NoError(t, err)
	require.NoError(t, file.Close())

	c.now = t0.Add(7 * time.Hour) // 停机期间已超过 6 小时
	reopened, expired := open(t, dir, c, 6*time.Hour)
	require.Equal(t, 1, reopened.Ignored())
	require.Len(t, expired, 1, "停机期间超期的项启动时被标记 gave_up，不会无声消失")
	require.Zero(t, reopened.PendingCount())

	// 新追加的行可以被回放，不会接在半行后面。
	require.NoError(t, reopened.RecordGaps(context.Background(), gap("ETH", 1, "a")))
	require.NoError(t, reopened.Close())
	final, _ := open(t, dir, c, 0)
	require.Equal(t, 1, final.PendingCount())
	require.Equal(t, "ETH", final.Pending()[0].Subject)
}

// 写盘失败时内存仍是权威（本进程内的恢复不受影响），并标记降级。
func TestLedgerDiskFailureKeepsMemoryAndMarksDegraded(t *testing.T) {
	dir, c := t.TempDir(), &clock{now: t0}
	ledger, _ := open(t, dir, c, 0)
	require.NoError(t, os.Remove(dir))
	require.NoError(t, os.WriteFile(dir, []byte("not a directory"), 0o600)) // 目录被同名文件占用，之后无法再创建账本文件
	err := ledger.RecordGaps(context.Background(), gap("BTC", 0, "a"))
	require.Error(t, err)
	require.True(t, ledger.Degraded())
	require.Equal(t, 1, ledger.PendingCount(), "内存里的待办不因写盘失败丢失")
}

// Reset 只删账本文件，不碰目录里的其他东西；之后重新打开得到空的待恢复集合；目录不存在不算错。
func TestResetRemovesLedgerFilesOnly(t *testing.T) {
	dir := t.TempDir()
	c := &clock{now: t0}
	ledger, _ := open(t, dir, c, 0)
	require.NoError(t, ledger.RecordGaps(context.Background(), gap("BTC-USDT", 0, "f1", "f2")))
	require.Equal(t, 2, ledger.PendingCount())
	require.NoError(t, ledger.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "factor-gap-202609.jsonl"), []byte("{}\n"), 0o640))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o640))

	removed, err := Reset(dir)
	require.NoError(t, err)
	require.Equal(t, 2, removed, "当月与上月两个账本文件")
	require.FileExists(t, filepath.Join(dir, "notes.txt"))

	reopened, expired := open(t, dir, c, time.Hour)
	require.Empty(t, expired)
	require.Zero(t, reopened.PendingCount(), "重置后没有待恢复项")

	none, err := Reset(filepath.Join(dir, "missing"))
	require.NoError(t, err)
	require.Zero(t, none)
	_, err = Reset(" ")
	require.Error(t, err)
}

// 预热跳过的行带 reason=warming，回放后原因保留；空洞计数只统计真正的空洞。
func TestLedgerKeepsWarmingReasonAndCountsOnlyGapSkips(t *testing.T) {
	dir, c := t.TempDir(), &clock{now: t0}
	ledger, _ := open(t, dir, c, 0)
	warming := []pipeline.GapEntry{
		{SetID: "fset", Subject: "ETH", Period: t0, FactorID: "a", DefinitionHash: "h-a", TriggerEventID: "evt", Reason: pipeline.SkipReasonWarming},
		{SetID: "fset", Subject: "ETH", Period: t0, FactorID: "b", DefinitionHash: "h-b", TriggerEventID: "evt", Reason: pipeline.SkipReasonWarming},
	}
	require.NoError(t, ledger.RecordGaps(context.Background(), append(gap("BTC", 0, "a"), warming...)))
	require.Equal(t, 3, ledger.PendingCount())
	require.EqualValues(t, 1, ledger.SkippedTotal(), "预热跳过不计入空洞计数")
	require.NoError(t, ledger.Close())

	reopened, _ := open(t, dir, c, 0)
	reasons := map[string]string{}
	for _, entry := range reopened.Pending() {
		reasons[entry.Subject+"|"+entry.FactorID] = entry.Reason
	}
	require.Equal(t, map[string]string{"BTC|a": "", "ETH|a": pipeline.SkipReasonWarming, "ETH|b": pipeline.SkipReasonWarming}, reasons)
}

// HasPendingIn 只看指定集合、指定周期是否还有待办；终结后即为 false。
func TestLedgerHasPendingInPeriod(t *testing.T) {
	ledger, _ := open(t, t.TempDir(), &clock{now: t0}, 0)
	require.NoError(t, ledger.RecordGaps(context.Background(), append(gap("BTC", 0, "a"), gap("ETH", 1, "a")...)))
	require.True(t, ledger.HasPendingIn("fset", t0))
	require.True(t, ledger.HasPendingIn("fset", t0.Add(time.Minute).In(time.FixedZone("UTC+8", 8*3600))))
	require.False(t, ledger.HasPendingIn("other", t0))
	require.False(t, ledger.HasPendingIn("fset", t0.Add(2*time.Minute)))

	var refs []Ref
	for _, entry := range ledger.Pending() {
		if entry.Period.Equal(t0) {
			refs = append(refs, entry.Ref())
		}
	}
	require.NoError(t, ledger.MarkRepaired(refs))
	require.False(t, ledger.HasPendingIn("fset", t0))
	require.True(t, ledger.HasPendingIn("fset", t0.Add(time.Minute)))
}

func TestLedgerClearRemovesFilesAndKeepsWorking(t *testing.T) {
	dir := t.TempDir()
	ledger, _, err := Open(dir, time.Now, 0)
	require.NoError(t, err)
	require.NoError(t, ledger.RecordGaps(context.Background(), []pipeline.GapEntry{{SetID: "s", Subject: "A", Period: time.Unix(60, 0).UTC(), FactorID: "f", DefinitionHash: "h"}}))
	require.Equal(t, 1, ledger.PendingCount())

	removed, err := ledger.Clear()
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.Zero(t, ledger.PendingCount())
	files, _ := filepath.Glob(filepath.Join(dir, "factor-gap-*.jsonl"))
	require.Empty(t, files)

	require.NoError(t, ledger.RecordGaps(context.Background(), []pipeline.GapEntry{{SetID: "s", Subject: "B", Period: time.Unix(120, 0).UTC(), FactorID: "f", DefinitionHash: "h"}}))
	require.Equal(t, 1, ledger.PendingCount(), "清空后可以继续记账")
}
