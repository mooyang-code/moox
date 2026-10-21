package marketwiring

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
)

func NewMarketKlinePipeline(storage marketfetch.Storage, marketID string, instrumentType marketdata.InstrumentType, providerID, sourceID string) (*marketfetch.KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("market kline storage is required")
	}
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	if sourceID == "" {
		sourceID = DefaultSourceID(providerID, string(instrumentType))
	}
	if marketID == "" || providerID == "" || sourceID == "" {
		return nil, fmt.Errorf("market, provider and source are required")
	}
	if marketID == marketfetch.StockCNSpaceID {
		if instrumentType == marketdata.InstrumentEquity {
			return NewStockKlinePipelineForSource(storage, providerID, sourceID)
		}
	}
	provider, err := newMarketProvider(marketID, instrumentType, providerID, sourceID)
	if err != nil {
		return nil, err
	}
	registry := marketdata.NewRegistry()
	if err := registry.Register(provider); err != nil {
		return nil, err
	}
	router, err := marketdata.NewRouter(registry, marketfetch.RuntimeKlineBreakerThreshold(), nil, nil)
	if err != nil {
		return nil, err
	}
	// Each collection task writes its own single-frequency Dataset, so a
	// shared market pipeline takes the Dataset from the request.
	pipeline := &marketfetch.KlinePipeline{
		Router: router, Storage: storage, CandidateChain: []string{providerID},
		RouteID: marketID + "_" + string(instrumentType) + "_kline",
		SpaceID: marketID, MarketID: marketID, InstrumentType: instrumentType,
		SourceID: sourceID,
	}
	if marketID == "crypto" {
		pipeline.InstrumentType = marketdata.InstrumentSpot
		if instrumentType == marketdata.InstrumentSwap {
			pipeline.InstrumentType = marketdata.InstrumentSwap
		}
	}
	if marketID == marketfetch.StockCNSpaceID {
		if instrumentType == marketdata.InstrumentIndex {
			pipeline.InstrumentType = marketdata.InstrumentIndex
		} else {
			pipeline.InstrumentType = marketdata.InstrumentConvertibleBond
		}
		pipeline.Calendar, err = loadStockCNCalendar()
		if err != nil {
			return nil, err
		}
	}
	return pipeline, nil
}

// newMarketProvider 从行情源目录构造 SCF 与本地执行端取数用的数据源（加密、可转债、指数）。
func newMarketProvider(marketID string, instrumentType marketdata.InstrumentType, providerID, sourceID string) (marketdata.MarketProvider, error) {
	market := marketdata.MarketID(marketID)
	entry, err := catalog.Default().Lookup(market, instrumentType, providerID, sourceID)
	if err != nil || entry.RouteSource {
		return nil, unsupportedMarketSource(market, instrumentType, providerID, sourceID)
	}
	return entry.Build(marketdata.Deps{HTTPClient: httpclient.SharedClientIfEnabled(), SourceID: sourceID})
}

// unsupportedMarketSource 是目录里没有这个组合时的报错，按市场与品种说明哪一层不支持。
func unsupportedMarketSource(market marketdata.MarketID, instrumentType marketdata.InstrumentType, providerID, sourceID string) error {
	switch {
	case market == "crypto" && providerID == "binance" && instrumentType != marketdata.InstrumentSpot && instrumentType != marketdata.InstrumentSwap:
		return fmt.Errorf("unsupported crypto instrument source %s/%s", providerID, sourceID)
	case market == "crypto" && providerID == "binance":
		return fmt.Errorf("unsupported crypto %s source %s/%s", instrumentType, providerID, sourceID)
	case instrumentType == marketdata.InstrumentConvertibleBond:
		return fmt.Errorf("unsupported convertible_bond source %s/%s", providerID, sourceID)
	case instrumentType == marketdata.InstrumentIndex && market != marketfetch.StockCNSpaceID:
		return fmt.Errorf("index market %q is unsupported", market)
	case instrumentType == marketdata.InstrumentIndex:
		return fmt.Errorf("unsupported index source %s/%s", providerID, sourceID)
	}
	return fmt.Errorf("unsupported market %q", market)
}
