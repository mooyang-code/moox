package pipeline

import (
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// 回放取值与实时组装共用有限数值规整：NaN、+Inf、-Inf 和 nil 一律视为缺失不输出，有限数值原样保留。
func TestPeriodValuesOmitsNonFiniteAndNilValues(t *testing.T) {
	period := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	def := domain.FactorDef{
		FactorID:   "f",
		FactorType: domain.FactorTypeTimeSeries,
		Outputs:    []string{"ok_val", "nan_val", "pos_inf", "neg_inf", "nil_val"},
	}
	member := domain.MemberDef{Key: "f", Factor: def, Kind: domain.MemberKindFixed}
	plan := Plan{Members: []domain.MemberDef{member}}
	cols := append([]string{"data_time", "series_tag"}, member.OutputColumns()...)
	computation := Computation{
		Results: map[string]map[string]ItemResult{
			"f": {
				"BTC": {
					Member:  "f",
					Columns: cols,
					Rows: [][]any{
						{period, "", 1.5, math.NaN(), math.Inf(1), math.Inf(-1), nil},
					},
				},
			},
		},
	}

	got, err := PeriodValues(plan, computation, period)
	require.NoError(t, err)
	fields := got[seriesset.Series{SubjectID: "BTC", SeriesTag: ""}]["f"]
	require.Equal(t, map[string]float64{"f__ok_val": 1.5}, fields)
	require.Nil(t, normalizeNumeric(math.NaN()))
	require.Nil(t, normalizeNumeric(math.Inf(1)))
	require.Nil(t, normalizeNumeric(math.Inf(-1)))
	require.Nil(t, normalizeNumeric(nil))
	require.Equal(t, 1.5, normalizeNumeric(1.5))
}
