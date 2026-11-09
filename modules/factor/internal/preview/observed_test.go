package preview

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// allSource 还能列出源数据集登记过的全部标的（含已下架的）。
type allSource struct {
	fakeSource
	all []string
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
