package watermark

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStorePersistsStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine", "watermarks.json")
	store, err := Open(path)
	require.NoError(t, err)
	_, ok := store.Get("s")
	require.False(t, ok)
	at := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	require.NoError(t, store.Put(State{TaskID: "s", CommittedThrough: at, Target: at.Add(time.Minute), UniverseSubjects: []string{"A"}}))
	reopened, err := Open(path)
	require.NoError(t, err)
	state, ok := reopened.Get("s")
	require.True(t, ok)
	require.True(t, state.Behind())
	require.Equal(t, []string{"A"}, state.UniverseSubjects)
	require.Len(t, reopened.All(), 1)
}

// 目标只往后推：整份保存带着更早的目标时保留已存的；RaiseTarget 只改目标、不动已提交的位置，没有记录的任务不建立。
func TestTargetOnlyMovesForward(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "watermarks.json"))
	require.NoError(t, err)
	base := time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC)
	require.NoError(t, store.Put(State{TaskID: "a", CommittedThrough: base, Target: base}))

	stale, _ := store.Get("a")
	require.NoError(t, store.RaiseTarget("a", base.Add(55*time.Minute)))
	stale.CommittedThrough = base.Add(5 * time.Minute)
	require.NoError(t, store.Put(stale), "追赶循环拿着旧副本写回")
	got, _ := store.Get("a")
	require.Equal(t, base.Add(55*time.Minute), got.Target, "推上去的目标没有被旧副本盖回")
	require.Equal(t, base.Add(5*time.Minute), got.CommittedThrough)

	require.NoError(t, store.RaiseTarget("a", base))
	got, _ = store.Get("a")
	require.Equal(t, base.Add(55*time.Minute), got.Target, "更早的目标不回拉")
	require.Equal(t, base.Add(5*time.Minute), got.CommittedThrough, "RaiseTarget 不动已提交的位置")

	require.NoError(t, store.RaiseTarget("b", base))
	_, ok := store.Get("b")
	require.False(t, ok, "没有记录的任务不建立")
}
