package selection

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// Normalize calculates a cross-sectional value for one factor. The sample is
// exactly rows, which must already be the rule's post-filter-before-score
// candidates. Raw input values are never overwritten.
func Normalize(rows []Row, factor, method string) (map[string]float64, error) {
	internal := make([]row, 0, len(rows))
	for _, item := range rows {
		value, ok := item.Values[factor]
		if !ok {
			return nil, fmt.Errorf("%s(%s): factor value is missing for %s", method, factor, item.InstrumentID)
		}
		parsed, err := strconv.ParseFloat(value.String(), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, fmt.Errorf("%s(%s): factor value for %s is not finite", method, factor, item.InstrumentID)
		}
		internal = append(internal, row{id: item.InstrumentID, values: map[string]quant.Decimal{factor: value}})
	}
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "pct_rank", "pctrank":
		return pctRankForRows(internal, factor), nil
	case "zscore", "z_score":
		return zScoreForRows(internal, factor), nil
	default:
		return nil, fmt.Errorf("unsupported normalization method %q", method)
	}
}
