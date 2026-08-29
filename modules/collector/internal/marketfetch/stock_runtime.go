package marketfetch

import (
	"fmt"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoney"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sina"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tencent"
)

func NewStockKlinePipeline(storage Storage) (*KlinePipeline, error) {
	registry := marketdata.NewRegistry()
	for _, provider := range []marketdata.MarketProvider{
		sina.New(sina.Config{}),
		tencent.New(tencent.Config{}),
		eastmoney.New(eastmoney.Config{}),
	} {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	router, err := marketdata.NewRouter(registry, 3, nil, nil)
	if err != nil {
		return nil, err
	}
	chain := strings.FieldsFunc(os.Getenv("MOOX_MARKET_FETCH_PROVIDER_CHAIN"), func(r rune) bool { return r == ',' || r == '|' })
	if len(chain) == 0 {
		chain = []string{"sina", "tencent", "eastmoney"}
	}
	return &KlinePipeline{Router: router, Storage: storage, CandidateChain: chain, RouteID: StockCNRouteID}, nil
}

// NewMarketStorage keeps one authenticated Storage adapter for both market
// modules while the remaining Binance-specific configuration is retired.
func NewMarketStorage(target, writeSource string) (Storage, error) {
	storage, err := binance.NewBatchStorageWithWriteSource(target, binance.InstTypeSPOT, writeSource)
	if err != nil {
		return nil, fmt.Errorf("create market storage: %w", err)
	}
	return storage, nil
}
