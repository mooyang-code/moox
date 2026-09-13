package storagepb

import "testing"

func TestSeriesWindowSubjectLimit(t *testing.T) {
	for _, tc := range []struct{ rows, columns, want int }{
		{10000, 46, 1}, {10000, 47, 0}, {10000, 6, 5}, {10000, 7, 4},
		{2, 1, 512}, {10001, 1, 0}, {0, 1, 0}, {1, -1, 0},
	} {
		if got := SeriesWindowSubjectLimit(tc.rows, tc.columns); got != tc.want {
			t.Errorf("rows=%d columns=%d: got %d want %d", tc.rows, tc.columns, got, tc.want)
		}
	}
}
