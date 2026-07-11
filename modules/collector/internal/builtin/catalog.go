package builtin

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	cryptomarket "github.com/mooyang-code/moox/modules/collector/internal/markets/crypto"
	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockus"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	binanceprovider "github.com/mooyang-code/moox/modules/collector/internal/providers/binance"
	ifengprovider "github.com/mooyang-code/moox/modules/collector/internal/providers/ifeng"
	okxprovider "github.com/mooyang-code/moox/modules/collector/internal/providers/okx"
	tdxprovider "github.com/mooyang-code/moox/modules/collector/internal/providers/tdx"
	tencentprovider "github.com/mooyang-code/moox/modules/collector/internal/providers/tencent"
	"github.com/mooyang-code/moox/packages/marketmanifest"
)

type Catalog struct {
	MarketFactories   map[marketdata.MarketID]func() (markets.Module, error)
	ProviderFactories map[marketdata.ProviderID]func() providers.KlineProvider
}

func Default(calendarPath string) Catalog {
	return Catalog{MarketFactories: map[marketdata.MarketID]func() (markets.Module, error){"stock_cn": func() (markets.Module, error) {
		calendar, err := stockcn.LoadCalendar(calendarPath)
		if err != nil {
			return nil, err
		}
		return stockcn.New(calendar), nil
	}, "stock_us": func() (markets.Module, error) { return stockus.New(), nil }, "crypto_binance": func() (markets.Module, error) { return cryptomarket.New("crypto_binance", "BINANCE"), nil }, "crypto_okx": func() (markets.Module, error) { return cryptomarket.New("crypto_okx", "OKX"), nil }}, ProviderFactories: map[marketdata.ProviderID]func() providers.KlineProvider{"binance": func() providers.KlineProvider { return binanceprovider.New(binanceprovider.Config{}) }, "okx": func() providers.KlineProvider { return okxprovider.New(okxprovider.Config{}) }, "ifeng": func() providers.KlineProvider { return ifengprovider.New(ifengprovider.Config{}) }, "tencent": func() providers.KlineProvider { return tencentprovider.New(tencentprovider.Config{}) }, "tdx": func() providers.KlineProvider { return tdxprovider.New(tdxprovider.Config{}) }}}
}
func (c Catalog) Provider(id marketdata.ProviderID) (providers.KlineProvider, error) {
	factory := c.ProviderFactories[id]
	if factory == nil {
		return nil, fmt.Errorf("provider %q is not built in", id)
	}
	return factory(), nil
}
func (c Catalog) Market(id marketdata.MarketID) (markets.Module, error) {
	factory := c.MarketFactories[id]
	if factory == nil {
		return nil, fmt.Errorf("market %q is not built in", id)
	}
	return factory()
}
func (c Catalog) ProviderIDs() []string {
	ids := make([]string, 0, len(c.ProviderFactories))
	for id := range c.ProviderFactories {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	return ids
}
func (c Catalog) ValidateManifests(manifests []marketmanifest.Manifest) error {
	for _, manifest := range manifests {
		if c.MarketFactories[marketdata.MarketID(manifest.MarketID)] == nil {
			return fmt.Errorf("market %q has no built-in factory", manifest.MarketID)
		}
		for _, provider := range manifest.Providers {
			if c.ProviderFactories[marketdata.ProviderID(provider.ID)] == nil {
				return fmt.Errorf("provider %q has no built-in factory", provider.ID)
			}
		}
	}
	return nil
}
