package store

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

func TestInstrumentRecordMinNotionalAt(t *testing.T) {
	price := shared.MustDecimal("50000")
	for name, test := range map[string]struct {
		record InstrumentRecord
		want   string
	}{
		"交易所给了最小名义金额（币安）":           {InstrumentRecord{MinNotional: "5", MinExchangeQuantity: "0.001", ContractValue: "1"}, "5"},
		"OKX 现货：minSz × 价格":         {InstrumentRecord{MinNotional: "0", MinExchangeQuantity: "0.00001", ContractValue: "1"}, "0.5"},
		"OKX 永续：minSz × ctVal × 价格": {InstrumentRecord{MinNotional: "0", MinExchangeQuantity: "1", ContractValue: "0.01"}, "500"},
		"没有最小下单量":                   {InstrumentRecord{ContractValue: "0.01"}, ""},
	} {
		got, err := test.record.MinNotionalAt(price)
		require.NoError(t, err, name)
		require.Equal(t, test.want, got, name)
	}
	_, err := InstrumentRecord{MinExchangeQuantity: "abc"}.MinNotionalAt(price)
	require.Error(t, err, "规则解析不了要报错")
}
