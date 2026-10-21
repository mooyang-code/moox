package marketwiring

import (
	"context"
	"slices"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
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

// NewSubjectListers 从行情源目录取出全部能提供标的快照的数据源：币安用传入的出口代理客户端，
// A 股只启用路由里 instrument 为 active 且数据源配置开启的。
func NewSubjectListers(clients ...*httpclient.HTTPClient) (subjectsync.Listers, error) {
	var http *httpclient.HTTPClient
	if len(clients) > 0 {
		http = clients[0]
	}
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return nil, err
	}
	providerConfigs, err := marketfetch.LoadStockCNProviderRuntime(route)
	if err != nil {
		return nil, err
	}
	sources, err := catalog.Default().Instruments(func(entry marketdata.CatalogEntry) (marketdata.Deps, bool) {
		if entry.MarketID != marketfetch.StockCNSpaceID {
			return marketdata.Deps{HTTPClient: http}, true
		}
		if entry.Instrument != marketdata.InstrumentEquity || !slices.Contains(route.InstrumentProviders(), entry.Key.ProviderID) {
			return marketdata.Deps{}, false
		}
		config, ok := providerConfigs[entry.Key.ProviderID]
		if !ok || !config.InstrumentEnabled {
			return marketdata.Deps{}, false
		}
		return marketdata.Deps{Settings: stockCNSettings(config)}, true
	})
	if err != nil {
		return nil, err
	}
	listers := subjectsync.Listers{}
	for _, source := range sources {
		listers[subjectsync.ListerKey{Source: source.Entry.Key.ProviderID, InstrumentType: string(source.Entry.Instrument)}] = fetcherLister{fetcher: source.Fetcher, marketID: source.Entry.MarketID}
	}
	return listers, nil
}
