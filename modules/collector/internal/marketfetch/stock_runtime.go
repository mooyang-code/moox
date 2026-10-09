package marketfetch

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// NewMarketStorageForMarket borrows the invocation's single Access client.
func NewMarketStorageForMarket(gateway gatewayclient.Invoker, marketType, writeSource string) (Storage, error) {
	if strings.EqualFold(strings.TrimSpace(marketType), "equity") || strings.EqualFold(strings.TrimSpace(marketType), StockCNSpaceID) {
		marketType = marketstorage.InstTypeSPOT
	}
	storage, err := marketstorage.NewGatewayBatchStorage(gateway, marketType, writeSource)
	if err != nil {
		return nil, fmt.Errorf("create market storage: %w", err)
	}
	return storage, nil
}
