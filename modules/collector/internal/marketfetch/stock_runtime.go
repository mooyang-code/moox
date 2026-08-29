package marketfetch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/baidu"
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
	calendar, err := loadStockCNCalendar()
	if err != nil {
		return nil, err
	}
	return &KlinePipeline{Router: router, Storage: storage, CandidateChain: chain, RouteID: StockCNRouteID, Calendar: calendar}, nil
}

func loadStockCNCalendar() (*stockmarket.Calendar, error) {
	candidates := []string{strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_CALENDAR_PATH")), "markets/stock_cn/calendar.yaml", "config/markets/stock_cn/calendar.yaml", "modules/collector/config/markets/stock_cn/calendar.yaml"}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Clean(candidate)); err != nil {
			continue
		}
		calendar, err := stockmarket.LoadCalendar(candidate)
		if err != nil {
			return nil, fmt.Errorf("load stock_cn calendar %s: %w", candidate, err)
		}
		return calendar, nil
	}
	return nil, fmt.Errorf("stock_cn calendar config was not found")
}

func NewStockInstrumentPipeline(storage InstrumentStorage) (*InstrumentPipeline, error) {
	registry := marketdata.NewRegistry()
	for _, provider := range []marketdata.MarketProvider{
		sina.New(sina.Config{}),
		eastmoney.New(eastmoney.Config{}),
		baidu.New(baidu.Config{}),
	} {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	chain := strings.FieldsFunc(os.Getenv("MOOX_INSTRUMENT_PROVIDER_CHAIN"), func(r rune) bool { return r == ',' || r == '|' })
	if len(chain) == 0 {
		chain = []string{"sina", "eastmoney", "baidu"}
	}
	return &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: chain, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, DataSourceID: StockCNDataSourceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}, MinimumCount: 4000}, nil
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

func NewStockInstrumentStorage(target, writeSource string) (InstrumentStorage, error) {
	storage, err := binance.NewBatchStorageWithWriteSource(target, binance.InstTypeSPOT, writeSource)
	if err != nil {
		return nil, fmt.Errorf("create stock instrument storage: %w", err)
	}
	return storage, nil
}
