package preview

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// allSource 还能列出源数据集登记过的全部标的（含已下架的）。
type allSource struct {
	fakeSource
	all []string
	// empty 是试算区间内没有行的标的（下架很久了）。
	empty map[string]bool
	asked [][]string
}

func (s *allSource) ReadWindow(ctx context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	if len(req.Columns) == 0 {
		// 与真实 Storage 一致：读取至少要带一列。
		return nil, errors.New("column_names are required")
	}
	s.asked = append(s.asked, append([]string(nil), req.Subjects...))
	frames, err := s.fakeSource.ReadWindow(ctx, req)
	for subject := range s.empty {
		if frame := frames[subject]; frame != nil {
			frame.Rows = nil
		}
	}
	return frames, err
}

func (s *allSource) ListAllDatasetSubjects(context.Context, string, string) ([]string, error) {
	return s.all, nil
}

// 试算支持 observed：不在册的 OLD 只参与历史截面，不展示；current 只用在册的标的。
func TestPreviewObservedUsesDelistedHistory(t *testing.T) {
	source := &allSource{fakeSource: fakeSource{subjects: []string{"BTC", "ETH"}}, all: []string{"BTC", "ETH", "OLD"}}
	service := New(source, 1)
	service.now = func() time.Time { return time.Date(2026, 10, 17, 8, 30, 30, 0, time.UTC) }
	for _, tc := range []struct {
		spec string
		want float64
	}{
		{"cross_section_history: observed\noutput: ts_mean(cs_rank(close), 3)\n", 1.0 / 3}, // BTC、ETH、OLD 的值为 1、2、3
		{"output: ts_mean(cs_rank(close), 3)\n", 0.5},
	} {
		def := domain.MustDefinition("draft", "draft", tc.spec)
		def.AllowPartialUniverse = true
		result, err := service.Preview(context.Background(), Request{Set: set, Factor: def, Periods: 2})
		require.NoError(t, err)
		require.Len(t, result.Rows, 4, "只展示在册的 BTC、ETH")
		for _, row := range result.Rows {
			require.NotEqual(t, "OLD", row.Subject)
			if row.Subject == "BTC" {
				require.InDelta(t, tc.want, *row.Values[0], 1e-12, tc.spec)
			}
		}
	}
}

// 试算区间内没有行的不在册标的不算历史标的：不计入标的上限与内存预检，也不读进视图。
func TestPreviewObservedSkipsSubjectsWithoutRows(t *testing.T) {
	source := &allSource{fakeSource: fakeSource{subjects: []string{"BTC", "ETH"}}, all: []string{"BTC", "ETH", "OLD", "ANCIENT"},
		empty: map[string]bool{"ANCIENT": true}}
	service := New(source, 1)
	service.now = func() time.Time { return time.Date(2026, 10, 17, 8, 30, 30, 0, time.UTC) }
	def := domain.MustDefinition("draft", "draft", "cross_section_history: observed\noutput: ts_mean(cs_rank(close), 3)\n")
	def.AllowPartialUniverse = true
	_, err := service.Preview(context.Background(), Request{Set: set, Factor: def, Periods: 2})
	require.NoError(t, err)
	last := source.asked[len(source.asked)-1]
	require.Equal(t, []string{"BTC", "ETH", "OLD"}, last, "正式读取只带区间内有行的历史标的")
}

// 候选很多时提前报错：候选数超过预算的 4 倍不读；确认有行的个数一超过预算就停止，不读完全部候选。
func TestPreviewObservedRejectsTooManyHistorySubjectsEarly(t *testing.T) {
	def := domain.MustDefinition("draft", "draft", "cross_section_history: observed\noutput: ts_mean(cs_rank(close), 3)\n")
	def.AllowPartialUniverse = true
	candidates := func(n int) []string {
		out := []string{"BTC", "ETH"}
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("OLD%05d", i))
		}
		return out
	}
	run := func(n int) (*allSource, error) {
		source := &allSource{fakeSource: fakeSource{subjects: []string{"BTC", "ETH"}}, all: candidates(n)}
		service := New(source, 1)
		service.now = func() time.Time { return time.Date(2026, 10, 17, 8, 30, 30, 0, time.UTC) }
		_, err := service.Preview(context.Background(), Request{Set: set, Factor: def, Periods: 2})
		return source, err
	}
	source, err := run(9000) // 预算 1998，候选超过 4 倍
	require.ErrorIs(t, err, pipeline.ErrHistoryTooMany)
	require.Empty(t, source.asked, "粗限不读取")

	source, err = run(3000) // 都有行：确认到第 10 批（2000 个）就超过预算
	require.ErrorIs(t, err, pipeline.ErrHistoryTooMany)
	require.Len(t, source.asked, 10, "超过预算即停止，不读完 15 批")
	for _, batch := range source.asked {
		require.LessOrEqual(t, len(batch), 200)
	}
}
