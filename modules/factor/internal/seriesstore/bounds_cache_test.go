package seriesstore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// SubjectBounds 缓存各标的的边界：写入之后只刷新写到的标的，结果与逐个 Bounds 相同；清空存储后缓存一并清空。
func TestSubjectBoundsCacheFollowsWrites(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "A", 0, 3, rowsOf(map[int]float64{0: 100, 1: 101, 2: 102}), false)
	write(t, s, "B", 1, 2, rowsOf(map[int]float64{1: 200}), false)
	got, err := s.SubjectBounds(testSrc)
	require.NoError(t, err)
	require.Equal(t, map[string]Bounds{"A": {Earliest: at(0), Latest: at(2)}, "B": {Earliest: at(1), Latest: at(1)}}, got)

	write(t, s, "A", 3, 4, rowsOf(map[int]float64{3: 103}), false)
	write(t, s, "C", 5, 6, rowsOf(map[int]float64{5: 300}), false)
	got, err = s.SubjectBounds(testSrc)
	require.NoError(t, err)
	require.Equal(t, Bounds{Earliest: at(0), Latest: at(3)}, got["A"], "写入后刷新")
	require.Equal(t, Bounds{Earliest: at(5), Latest: at(5)}, got["C"], "新标的出现")
	require.Len(t, got, 3)
	for subject, bounds := range got {
		direct, ok, err := s.Bounds(testSrc, subject)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, direct, bounds, subject)
	}
	// 返回的是副本。
	got["A"] = Bounds{}
	again, err := s.SubjectBounds(testSrc)
	require.NoError(t, err)
	require.Equal(t, at(3), again["A"].Latest)

	require.NoError(t, s.Wipe())
	got, err = s.SubjectBounds(testSrc)
	require.NoError(t, err)
	require.Empty(t, got)
}
