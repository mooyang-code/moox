package marketfetch

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"strings"
)

// NewMarketStorage keeps one authenticated Storage adapter for both market
// modules while the remaining Binance-specific configuration is retired.
func NewMarketStorage(target, writeSource string) (Storage, error) {
	storage, err := marketstorage.NewBatchStorageWithWriteSource(target, marketstorage.InstTypeSPOT, writeSource)
	if err != nil {
		return nil, fmt.Errorf("create market storage: %w", err)
	}
	return storage, nil
}

// NewMarketStorageForMarket is the composition-root storage factory. Storage
// authentication is shared by all market runtimes, while the stockcn market
// uses the stock binding instead of interpreting "equity" as a Binance
// product type.
func NewMarketStorageForMarket(target, marketType, writeSource string) (Storage, error) {
	var storage Storage
	if strings.EqualFold(strings.TrimSpace(marketType), "equity") || strings.EqualFold(strings.TrimSpace(marketType), StockCNSpaceID) {
		var err error
		storage, err = NewMarketStorage(target, writeSource)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		storage, err = marketstorage.NewBatchStorageWithWriteSource(target, marketType, writeSource)
		if err != nil {
			return nil, fmt.Errorf("create %s market storage: %w", strings.TrimSpace(marketType), err)
		}
	}
	return storage, nil
}
