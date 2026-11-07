package store

import (
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

// 交易所给的统一账户 uniMMR 随快照落库、读回来、转回交易所快照；没有时不写字段。
func TestSnapshotKeepsTheExchangeUniMMR(t *testing.T) {
	record := SnapshotFromExchange(exchange.AccountSnapshot{UniMMR: shared.MustDecimal("1.4321"), ExchangeEquity: shared.MustDecimal("200")})
	require.Equal(t, "1.4321", record.UniMMR)
	data, err := json.Marshal(record)
	require.NoError(t, err)
	var back TradingAccountSnapshot
	require.NoError(t, json.Unmarshal(data, &back))
	require.Equal(t, "1.4321", back.UniMMR)
	require.Equal(t, "1.4321", back.ToExchange().UniMMR.String())

	none := SnapshotFromExchange(exchange.AccountSnapshot{})
	require.Empty(t, none.UniMMR)
	data, err = json.Marshal(none)
	require.NoError(t, err)
	require.NotContains(t, string(data), "uni_mmr")
}
