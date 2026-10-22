package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func newFirstSeenRepository(t *testing.T, path string) (*SubjectFirstSeenRepository, *Store) {
	t.Helper()
	mgr, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	return NewSubjectFirstSeenRepository(mgr), mgr
}

func subjectSet(ids ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func TestSubjectFirstSeenBaselineThenNewSubjects(t *testing.T) {
	repo, _ := newFirstSeenRepository(t, filepath.Join(t.TempDir(), "monitor.db"))
	ctx := t.Context()
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

	// 数据集第一次被观察：全部是存量，不给宽限。
	fresh, err := repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC", "ETH"), t0)
	require.NoError(t, err)
	require.Empty(t, fresh)

	// 之后出现的对象记下首次出现时间，并且重复观察时保持不变。
	t1 := t0.Add(13 * time.Minute)
	fresh, err = repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC", "ETH", "NEW"), t1)
	require.NoError(t, err)
	require.Equal(t, map[string]time.Time{"NEW": t1}, fresh)
	fresh, err = repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC", "ETH", "NEW"), t1.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[string]time.Time{"NEW": t1}, fresh)

	// 其他数据集互不影响：它自己的第一次观察也是存量。
	fresh, err = repo.ObserveSubjects(ctx, "crypto", "other", subjectSet("BTC", "NEW"), t1)
	require.NoError(t, err)
	require.Empty(t, fresh)
}

func TestSubjectFirstSeenRetiredSubjectsGetGraceAgainWhenRelisted(t *testing.T) {
	repo, _ := newFirstSeenRepository(t, filepath.Join(t.TempDir(), "monitor.db"))
	ctx := t.Context()
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	_, err := repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC", "OLD"), t0)
	require.NoError(t, err)

	// OLD 下架：记录删除；重新上架时当作新对象。
	_, err = repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC"), t0.Add(time.Hour))
	require.NoError(t, err)
	t2 := t0.Add(2 * time.Hour)
	fresh, err := repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC", "OLD"), t2)
	require.NoError(t, err)
	require.Equal(t, map[string]time.Time{"OLD": t2}, fresh)
}

func TestSubjectFirstSeenEmptyCatalogDoesNotWipeHistory(t *testing.T) {
	repo, _ := newFirstSeenRepository(t, filepath.Join(t.TempDir(), "monitor.db"))
	ctx := t.Context()
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	_, err := repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC"), t0)
	require.NoError(t, err)
	t1 := t0.Add(time.Minute)
	_, err = repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC", "NEW"), t1)
	require.NoError(t, err)

	fresh, err := repo.ObserveSubjects(ctx, "crypto", "ds", map[string]struct{}{}, t1.Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, fresh)
	fresh, err = repo.ObserveSubjects(ctx, "crypto", "ds", subjectSet("BTC", "NEW"), t1.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, map[string]time.Time{"NEW": t1}, fresh)
}

// Monitor 重启（重新打开同一个库）后，新对象的首次出现时间不重新计时，存量对象仍是存量。
func TestSubjectFirstSeenSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	repo, mgr := newFirstSeenRepository(t, path)
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	_, err := repo.ObserveSubjects(t.Context(), "crypto", "ds", subjectSet("BTC"), t0)
	require.NoError(t, err)
	t1 := t0.Add(13 * time.Minute)
	_, err = repo.ObserveSubjects(t.Context(), "crypto", "ds", subjectSet("BTC", "NEW"), t1)
	require.NoError(t, err)
	require.NoError(t, mgr.Close())

	reopened, _ := newFirstSeenRepository(t, path)
	fresh, err := reopened.ObserveSubjects(t.Context(), "crypto", "ds", subjectSet("BTC", "NEW"), t1.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[string]time.Time{"NEW": t1}, fresh, "重启后 NEW 仍按原来的首次出现时间，BTC 仍是存量")
}
