package domain

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTimeSeriesDefinitionRejectsUnexecutableCellBudget(t *testing.T) {
	factor := FactorDef{FactorType: FactorTypeTimeSeries, LookbackPeriods: 10000}
	for i := range 46 {
		factor.InputColumns = append(factor.InputColumns, fmt.Sprintf("column%d", i))
	}
	require.NoError(t, ValidateFactorReadLimit(factor))
	factor.InputColumns = append(factor.InputColumns, "extra")
	require.ErrorContains(t, ValidateFactorReadLimit(factor), "cell budget")
	factor.LookbackPeriods = 100
	require.NoError(t, ValidateFactorReadLimit(factor))
}
