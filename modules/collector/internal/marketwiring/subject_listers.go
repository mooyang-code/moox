package marketwiring

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
)

type fetcherLister struct {
	fetcher  marketdata.InstrumentFetcher
	marketID marketdata.MarketID
}

func (l fetcherLister) List(ctx context.Context) ([]marketdata.Instrument, error) {
	snapshot, err := l.fetcher.FetchInstrumentSnapshot(ctx, marketdata.InstrumentRequest{MarketID: l.marketID, SnapshotAt: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	return snapshot.Instruments, nil
}

// NewSubjectListers 创建标的同步用的数据源列表。binanceHTTP 是访问币安的客户端，Collector 传入经出口代理的
// 客户端；为空时直连。
func NewSubjectListers(binanceHTTP *httpclient.HTTPClient) (subjectsync.Listers, error) {
	listers := subjectsync.Listers{
		{Source: "binance", InstrumentType: "spot"}: fetcherLister{fetcher: binance.NewMarketDataAdapter(binance.AdapterConfig{InstrumentType: marketdata.InstrumentSpot, HTTPClient: binanceHTTP}), marketID: "crypto"},
		{Source: "binance", InstrumentType: "swap"}: fetcherLister{fetcher: binance.NewMarketDataAdapter(binance.AdapterConfig{InstrumentType: marketdata.InstrumentSwap, HTTPClient: binanceHTTP}), marketID: "crypto"},
	}
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return nil, err
	}
	providerConfigs, err := marketfetch.LoadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	for _, providerID := range route.InstrumentProviders() {
		config, ok := providerConfigs[providerID]
		if !ok || !config.InstrumentEnabled {
			continue
		}
		provider, err := newStockCNProvider(providerID, config)
		if err != nil {
			return nil, err
		}
		fetcher, ok := provider.(marketdata.InstrumentFetcher)
		if !ok {
			continue
		}
		listers[subjectsync.ListerKey{Source: providerID, InstrumentType: "equity"}] = fetcherLister{fetcher: fetcher, marketID: "stockcn"}
	}
	return listers, nil
}
