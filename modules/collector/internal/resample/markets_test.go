package resample

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	// 生产里标的规则由装配入口（marketwiring）引用后登记；这个包不引用装配层，测试里直接引用两个市场的规则包。
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/crypto"
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/stretchr/testify/require"
)

// 列的形状按空间的默认品种定；未登记的空间报错，不猜成“只有成交额”或“带成交笔数”。
func TestHasTradeCountRejectsUnregisteredSpace(t *testing.T) {
	has, err := hasTradeCount("crypto")
	require.NoError(t, err)
	require.True(t, has)
	has, err = hasTradeCount("stockcn")
	require.NoError(t, err)
	require.False(t, has)
	_, err = hasTradeCount("no_such_space")
	require.ErrorIs(t, err, markets.ErrUnknownMarket)
}
