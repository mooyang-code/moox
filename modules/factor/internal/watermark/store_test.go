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

// Delete 与 Retain 删除指定或不在保留集合中的任务水位，并立即原子落盘。
func TestStoreDeleteAndRetainPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watermarks.json")
	store, err := Open(path)
	require.NoError(t, err)
	base := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	for _, id := range []string{"ftask_a", "ftask_b", "ftask_c"} {
		require.NoError(t, store.Put(State{TaskID: id, CommittedThrough: base, Target: base}))
	}

	deleted, err := store.Delete("ftask_b")
	require.NoError(t, err)
	require.True(t, deleted)
	deleted, err = store.Delete("ftask_b")
	require.NoError(t, err)
	require.False(t, deleted, "重复删除不存在的任务应返回 false")

	removed, err := store.Retain(map[string]struct{}{"ftask_a": {}})
	require.NoError(t, err)
	require.Equal(t, []string{"ftask_c"}, removed)

	reopened, err := Open(path)
	require.NoError(t, err)
	_, okA := reopened.Get("ftask_a")
	_, okB := reopened.Get("ftask_b")
	_, okC := reopened.Get("ftask_c")
	require.True(t, okA)
	require.False(t, okB)
	require.False(t, okC)
	require.Len(t, reopened.All(), 1)
}
