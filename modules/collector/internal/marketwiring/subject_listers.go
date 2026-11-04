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

type indexLister struct {
	fetcher marketdata.IndexConstituentFetcher
	index   string
}

func (l indexLister) List(ctx context.Context) ([]marketdata.Instrument, error) {
	snapshot, err := l.fetcher.FetchIndexConstituents(ctx, l.index)
	if err != nil {
		return nil, err
	}
	return snapshot.Instruments, nil
}

// NewSubjectListers 从行情源目录取出全部能提供标的快照的数据源：币安用传入的出口代理客户端，
// A 股只启用路由里 instrument 为 active 且数据源配置开启的。
// 能取指数成分股的数据源另外装进 IndexListers，按（数据源，指数）索引。
func NewSubjectListers(clients ...*httpclient.HTTPClient) (subjectsync.Listers, subjectsync.IndexListers, error) {
	var http *httpclient.HTTPClient
	if len(clients) > 0 {
		http = clients[0]
	}
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return nil, nil, err
	}
	providerConfigs, err := marketfetch.LoadStockCNProviderRuntime(route)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	listers := subjectsync.Listers{}
	indexes := subjectsync.IndexListers{}
	for _, source := range sources {
		listers[subjectsync.ListerKey{Source: source.Entry.Key.ProviderID, InstrumentType: string(source.Entry.Instrument)}] = fetcherLister{fetcher: source.Fetcher, marketID: source.Entry.MarketID}
		if fetcher, ok := source.Fetcher.(marketdata.IndexConstituentFetcher); ok {
			for _, index := range fetcher.IndexCodes() {
				indexes[subjectsync.IndexListerKey{Source: source.Entry.Key.ProviderID, Index: index}] = indexLister{fetcher: fetcher, index: index}
			}
		}
	}
	return listers, indexes, nil
}
