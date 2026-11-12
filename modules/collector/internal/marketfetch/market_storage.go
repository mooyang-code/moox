package marketfetch

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// NewMarketStorageForMarket borrows the invocation's single Access client.
// 股票没有现货、合约之分，用存储配置里 spot 那一份绑定。
func NewMarketStorageForMarket(gateway gatewayclient.Invoker, marketType string) (Storage, error) {
	if strings.EqualFold(strings.TrimSpace(marketType), string(marketdata.InstrumentEquity)) {
		marketType = marketstorage.InstTypeSPOT
	}
	storage, err := marketstorage.NewGatewayBatchStorage(gateway, marketType)
	if err != nil {
		return nil, fmt.Errorf("create market storage: %w", err)
	}
	return storage, nil
}
