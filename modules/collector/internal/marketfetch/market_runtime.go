package marketfetch

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/bond/eastmoney"
	bondsina "github.com/mooyang-code/moox/modules/collector/internal/sources/bond/sina"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/index/cni"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/index/csindex"
	indexeastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/index/eastmoney"
	indexsina "github.com/mooyang-code/moox/modules/collector/internal/sources/index/sina"
	indexsw "github.com/mooyang-code/moox/modules/collector/internal/sources/index/sw"
	indextencent "github.com/mooyang-code/moox/modules/collector/internal/sources/index/tencent"
	stockhkeastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/stockhk/eastmoney"
	stockhksina "github.com/mooyang-code/moox/modules/collector/internal/sources/stockhk/sina"
	stockuseastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/eastmoney"
	stockussina "github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/sina"
)

func NewMarketKlinePipeline(storage Storage, marketID string, instrumentType marketdata.InstrumentType, providerID, sourceID string) (*KlinePipeline, error) {
	if storage == nil {
		return nil, fmt.Errorf("market kline storage is required")
	}
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	if marketID == "" || providerID == "" || sourceID == "" {
		return nil, fmt.Errorf("market, provider and source are required")
	}
	if marketID == StockCNSpaceID {
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
	router, err := marketdata.NewRouter(registry, 2, nil, nil)
	if err != nil {
		return nil, err
	}
	datasetID := marketDatasetID(marketID, instrumentType)
	pipeline := &KlinePipeline{
		Router: router, Storage: storage, CandidateChain: []string{providerID},
		RouteID: marketID + "_" + string(instrumentType) + "_kline",
		SpaceID: marketID, MarketID: marketID, InstrumentType: instrumentType,
		DatasetID: datasetID, SourceID: sourceID,
	}
	if marketID == StockCNSpaceID {
		if instrumentType == marketdata.InstrumentIndex {
			pipeline.ProductType = marketdata.ProductIndex
		} else {
			pipeline.ProductType = marketdata.ProductConvertibleBond
		}
		pipeline.Calendar, err = loadStockCNCalendar()
		if err != nil {
			return nil, err
		}
	}
	return pipeline, nil
}

func newMarketProvider(marketID string, instrumentType marketdata.InstrumentType, providerID, sourceID string) (marketdata.MarketProvider, error) {
	if instrumentType == marketdata.InstrumentConvertibleBond {
		switch {
		case providerID == "eastmoney" && sourceID == "convertible_bond_http":
			return eastmoney.New(eastmoney.Config{}), nil
		case providerID == "sina" && sourceID == "convertible_bond_http":
			return bondsina.New(bondsina.Config{}), nil
		default:
			return nil, fmt.Errorf("unsupported convertible_bond source %s/%s", providerID, sourceID)
		}
	}
	if instrumentType == marketdata.InstrumentIndex {
		if marketID != StockCNSpaceID {
			return nil, fmt.Errorf("index market %q is unsupported", marketID)
		}
		switch {
		case providerID == "eastmoney" && sourceID == "index_http":
			return indexeastmoney.New(indexeastmoney.Config{}), nil
		case providerID == "sina" && sourceID == "index_http":
			return indexsina.New(indexsina.Config{}), nil
		case providerID == "tencent" && sourceID == "index_http":
			return indextencent.New(indextencent.Config{}), nil
		case providerID == "cni" && sourceID == "index_cni_http":
			return cni.New(cni.Config{}), nil
		case providerID == "sw" && sourceID == "index_sw_http":
			return indexsw.New(indexsw.Config{}), nil
		case providerID == "csindex" && sourceID == "index_http":
			return csindex.New(csindex.Config{}), nil
		default:
			return nil, fmt.Errorf("unsupported index source %s/%s", providerID, sourceID)
		}
	}
	if marketID == "stockhk" {
		switch {
		case providerID == "eastmoney" && sourceID == "stockhk_http":
			return stockhkeastmoney.New(stockhkeastmoney.Config{}), nil
		case providerID == "sina" && sourceID == "stockhk_http":
			return stockhksina.New(stockhksina.Config{}), nil
		default:
			return nil, fmt.Errorf("unsupported stockhk source %s/%s", providerID, sourceID)
		}
	}
	if marketID == "stockus" {
		switch {
		case providerID == "eastmoney" && sourceID == "stockus_http":
			return stockuseastmoney.New(stockuseastmoney.Config{}), nil
		case providerID == "sina" && sourceID == "stockus_http":
			return stockussina.New(stockussina.Config{}), nil
		default:
			return nil, fmt.Errorf("unsupported stockus source %s/%s", providerID, sourceID)
		}
	}
	return nil, fmt.Errorf("unsupported market %q", marketID)
}

func marketDatasetID(marketID string, instrumentType marketdata.InstrumentType) string {
	switch {
	case marketID == "stockhk" && instrumentType == marketdata.InstrumentEquity:
		return "dataset_stockhk_equity_kline"
	case marketID == "stockus" && instrumentType == marketdata.InstrumentEquity:
		return "dataset_stockus_equity_kline"
	case marketID == StockCNSpaceID && instrumentType == marketdata.InstrumentIndex:
		return "dataset_stockcn_index_kline"
	case marketID == StockCNSpaceID && instrumentType == marketdata.InstrumentConvertibleBond:
		return "dataset_stockcn_bond_kline"
	default:
		return marketID + "_" + string(instrumentType) + "_kline"
	}
}
