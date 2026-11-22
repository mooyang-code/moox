package seriessync

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

// 没有 fill 属性时，量类字段（含换手率、成交笔数）缺行填 0：停牌日不能沿用停牌前的换手率，否则筹码因子每天按旧换手率衰减。
func TestDefaultFillTreatsTurnoverRateAsQuantity(t *testing.T) {
	require.Equal(t, seriesstore.FillZero, DefaultFill("turnover_rate"))
	require.Equal(t, seriesstore.FillZero, DefaultFill("volume"))
	require.Equal(t, seriesstore.FillZero, DefaultFill("trade_num"))
	require.Equal(t, seriesstore.FillPrevClose, DefaultFill("close"))
	require.Equal(t, seriesstore.FillPrev, DefaultFill("adj_factor"))
}
