package checkpoint

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreSavesAndListsProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "importer.db")
	s, err := Open(path)
	require.NoError(t, err)
	got, err := s.Lookup("job", "a.csv", "factor")
	require.NoError(t, err)
	require.Equal(t, Progress{}, got)

	require.NoError(t, s.Store("job", "a.csv", "factor", Progress{BatchesDone: 2, RowsDone: 2000}))
	require.NoError(t, s.Store("job", "a.csv", "factor", Progress{BatchesDone: 3, RowsDone: 2500, Status: "done"}))
	require.NoError(t, s.Save("job", "a.csv", "storage", 1, 10, false, "boom"))
	require.NoError(t, s.Close())

	// 重新打开：断点还在，建表语句可重复执行。
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	got, err = s.Lookup("job", "a.csv", "factor")
	require.NoError(t, err)
	require.Equal(t, Progress{BatchesDone: 3, RowsDone: 2500, Status: "done"}, got)
	list, err := s.List("job")
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "storage", list[1].Target)
	require.Equal(t, "boom", list[1].Error)
	jobs, err := s.Jobs()
	require.NoError(t, err)
	require.Equal(t, []string{"job"}, jobs)
}

func TestStoreImplementsRunnerContract(t *testing.T) {
	s, err := Open(":memory:")
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.Save("j", "f", "t", 2, 20, false, ""))
	b, r, done, err := s.Get("j", "f", "t")
	require.NoError(t, err)
	require.Equal(t, []any{int64(2), int64(20), false}, []any{b, r, done})
	require.NoError(t, s.Save("j", "f", "t", 3, 30, true, ""))
	_, _, done, _ = s.Get("j", "f", "t")
	require.True(t, done)
}
