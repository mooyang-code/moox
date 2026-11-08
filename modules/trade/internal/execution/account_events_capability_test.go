package execution

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

// noStreamAdapter 只有执行适配器的方法，没有私有流。
type noStreamAdapter struct{ ExecutionAdapter }

// 合成适配器要按子适配器真实能力判断有没有私有流：任何一个市场没有，整体就没有，会话改用轮询，不会反复订阅失败。
func TestAccountEventsOfFollowsEverySubAdapter(t *testing.T) {
	markets := []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
	all, err := NewMultiMarket(exchange.ExchangeBinance, markets, map[exchange.MarketType]ExecutionAdapter{
		exchange.MarketTypeSpot: &marketStub{market: exchange.MarketTypeSpot}, exchange.MarketTypeSwap: &marketStub{market: exchange.MarketTypeSwap},
	})
	require.NoError(t, err)
	require.NotNil(t, AccountEventsOf(all))

	partial, err := NewMultiMarket(exchange.ExchangeBinance, markets, map[exchange.MarketType]ExecutionAdapter{
		exchange.MarketTypeSpot: &marketStub{market: exchange.MarketTypeSpot}, exchange.MarketTypeSwap: noStreamAdapter{},
	})
	require.NoError(t, err)
	require.Nil(t, AccountEventsOf(partial), "合约子适配器没有私有流")
	require.Nil(t, AccountEventsOf(noStreamAdapter{}))
}
