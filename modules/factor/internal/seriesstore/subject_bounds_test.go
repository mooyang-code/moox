package seriesstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// SubjectBounds 一次扫描给出各标的的边界（各 series_tag 取并集），与逐个 Bounds 相同；写入后立即反映，清空后为空。
func TestSubjectBoundsScansAllSubjects(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "A", 0, 3, rowsOf(map[int]float64{0: 100, 1: 101, 2: 102}), false)
	rows := NewColumns(testFields, 3)
	bar(rows, 1, "spot", 200)
	bar(rows, 2, "spot", 201)
	bar(rows, 1, "swap", 300)
	_, err := s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "B", From: at(1), To: at(3), Rows: rows}, WriteOptions{FillGaps: true})
	require.NoError(t, err)

	got, err := s.SubjectBounds(testSrc)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for subject, bounds := range got {
		direct, ok, err := s.Bounds(testSrc, subject)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, direct, bounds, subject)
	}
	require.Equal(t, Bounds{Earliest: at(1), Latest: at(2)}, got["B"], "两条序列取并集")

	write(t, s, "A", 3, 4, rowsOf(map[int]float64{3: 103}), false)
	got, err = s.SubjectBounds(testSrc)
	require.NoError(t, err)
	require.Equal(t, at(3), got["A"].Latest, "写入后立即反映")

	require.NoError(t, s.Wipe())
	got, err = s.SubjectBounds(testSrc)
	require.NoError(t, err)
	require.Empty(t, got)
}
