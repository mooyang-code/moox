package markets

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/convertiblebond/eastmoney"
	indexcni "github.com/mooyang-code/moox/modules/collector/internal/sources/index/cni"
	indexeastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/index/eastmoney"
	indexsw "github.com/mooyang-code/moox/modules/collector/internal/sources/index/sw"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
	stockcneastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/eastmoney"
	stockcnsina "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/sina"
	stockcntdx "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tdx"
	stockcntencent "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tencent"
	stockcnths "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/ths"
	stockhkeastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/stockhk/eastmoney"
	stockuseastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/eastmoney"
	"github.com/mooyang-code/moox/packages/marketmanifest"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
)

// Composition contains the canonical catalog and the concrete sources that a
// process may dispatch. Construction is explicit so tests can inject HTTP and
// TDX transports without changing the registry contract.
type Composition struct {
	Catalog  *marketmanifest.Catalog
	Registry *sources.ProviderRegistry
}

func NewComposition(httpGetter markethttp.Getter, normalTDX *tdxwire.NormalClient, includeBinance bool, routes marketdata.RouteIPProvider) (*Composition, error) {
	rawGetter, ok := httpGetter.(markethttp.RawGetter)
	if !ok {
		return nil, fmt.Errorf("market composition requires an HTTP getter with raw response streaming")
	}
	catalog, err := marketmanifest.DefaultCatalog()
	if err != nil {
		return nil, fmt.Errorf("load market catalog: %w", err)
	}
	registry := sources.NewProviderRegistry()
	stockCN := stockcneastmoney.NewClient(httpGetter)
	stockHK := stockhkeastmoney.NewClient(httpGetter)
	stockUS := stockuseastmoney.NewClient(httpGetter)
	index := indexeastmoney.NewClient(httpGetter)
	cni := indexcni.NewClient(httpGetter)
	sw := indexsw.NewClient(httpGetter)
	convertibleBond := eastmoney.NewClient(httpGetter)
	registrations := []sources.ProviderRegistration{
		{Descriptor: stockCN.Descriptor(), Klines: stockCN},
		{Descriptor: stockHK.Descriptor(), Klines: stockHK},
		{Descriptor: stockUS.Descriptor(), Klines: stockUS},
		{Descriptor: index.Descriptor(), Klines: index},
		{Descriptor: cni.Descriptor(), Klines: cni},
		{Descriptor: sw.Descriptor(), Klines: sw},
		{Descriptor: convertibleBond.Descriptor(), Klines: convertibleBond},
	}
	tencent := stockcntencent.NewClient(rawGetter)
	registrations = append(registrations, sources.ProviderRegistration{Descriptor: tencent.Descriptor(), Klines: tencent})
	ths := stockcnths.NewClient(rawGetter)
	registrations = append(registrations, sources.ProviderRegistration{Descriptor: ths.Descriptor(), Klines: ths})
	sina := stockcnsina.NewClient(rawGetter)
	registrations = append(registrations, sources.ProviderRegistration{Descriptor: sina.Descriptor(), Klines: sina})
	for _, marketID := range []string{"stock_cn", "stock_hk", "stock_us"} {
		sinaDaily := stockcnsina.NewDailyClient(rawGetter, marketID)
		registrations = append(registrations, sources.ProviderRegistration{Descriptor: sinaDaily.Descriptor(), Klines: sinaDaily})
	}
	if normalTDX != nil {
		tdxClient := stockcntdx.NewMultiClient(normalTDX, []string{"stock_cn"}, []string{"equity", "index", "convertible_bond"})
		registrations = append(registrations, sources.ProviderRegistration{Descriptor: tdxClient.Descriptor(), Klines: tdxClient, Instruments: tdxClient})
	}
	if includeBinance {
		for _, instrumentType := range []string{binance.InstTypeSPOT, binance.InstTypeSWAP} {
			fetcher := binance.NewMarketDataFetcher(instrumentType)
			fetcher.Routes = routes
			instruments := binance.NewInstrumentSnapshotFetcher(instrumentType, routes)
			registrations = append(registrations, sources.ProviderRegistration{Descriptor: fetcher.Descriptor(), Klines: fetcher, Instruments: instruments})
		}
	}
	for _, registration := range registrations {
		if err := registry.Register(registration); err != nil {
			return nil, fmt.Errorf("register %s: %w", registration.Descriptor.Key(), err)
		}
	}
	return &Composition{Catalog: catalog, Registry: registry}, nil
}
