package marketwiring

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
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

// NewSubjectListers 遍历行情源目录里所有市场的登记项，装出圈选标的用的两张表，键都带市场，互不依赖：
// 实现了 InstrumentFetcher 的进“全部标的”表（Listers），实现了 IndexConstituentFetcher 的进“指数成分”表（IndexListers）。
// 请求走直连还是出口代理看市场 market.yaml 的 subject_egress（走代理的用传入的客户端）；
// 来源按路由分配的登记项，只启用路由 instrument_providers 里列出、且数据源配置开启了 instrument 的。
func NewSubjectListers(clients ...*httpclient.HTTPClient) (subjectsync.Listers, subjectsync.IndexListers, error) {
	var proxy *httpclient.HTTPClient
	if len(clients) > 0 {
		proxy = clients[0]
	}
	listers := subjectsync.Listers{}
	indexes := subjectsync.IndexListers{}
	for _, entry := range catalog.Default().Entries() {
		marketID := string(entry.MarketID)
		config, err := markets.ConfigFor(marketID)
		if err != nil {
			return nil, nil, err
		}
		deps := marketdata.Deps{}
		if config.SubjectEgress == markets.SubjectEgressProxy {
			deps.HTTPClient = proxy
		}
		if entry.RouteSource {
			settings, enabled, err := routedInstrumentSettings(entry)
			if err != nil {
				return nil, nil, err
			}
			if !enabled {
				continue
			}
			deps.Settings = settings
		}
		provider, err := entry.Build(deps)
		if err != nil {
			return nil, nil, err
		}
		source := entry.Key.ProviderID
		if fetcher, ok := provider.(marketdata.InstrumentFetcher); ok {
			listers[subjectsync.ListerKey{MarketID: marketID, Source: source, InstrumentType: string(entry.Instrument)}] = fetcherLister{fetcher: fetcher, marketID: entry.MarketID}
		}
		if fetcher, ok := provider.(marketdata.IndexConstituentFetcher); ok {
			for _, index := range fetcher.IndexCodes() {
				indexes[subjectsync.IndexListerKey{MarketID: marketID, Source: source, Index: index}] = indexLister{fetcher: fetcher, index: index}
			}
		}
	}
	return listers, indexes, nil
}

// routedInstrumentSettings 返回按路由分配来源的登记项做标的同步时的运行参数；路由没把它列为标的来源、
// 或数据源配置没开启 instrument 时 enabled 为 false。
func routedInstrumentSettings(entry marketdata.CatalogEntry) (marketdata.SourceSettings, bool, error) {
	route, err := marketfetch.LoadStockCNRoute()
	if err != nil {
		return marketdata.SourceSettings{}, false, err
	}
	if route.MarketID != string(entry.MarketID) {
		return marketdata.SourceSettings{}, false, fmt.Errorf("市场 %s 的数据源 %s 按路由分配来源，但没有它的路由文件", entry.MarketID, entry.Key.ProviderID)
	}
	if !slices.Contains(route.InstrumentProviders(), entry.Key.ProviderID) {
		return marketdata.SourceSettings{}, false, nil
	}
	config, found, err := marketfetch.LoadStockCNSourceRuntime(entry.Key.ProviderID)
	if err != nil || !found || !config.InstrumentEnabled {
		return marketdata.SourceSettings{}, false, err
	}
	return stockCNSettings(config), true, nil
}
