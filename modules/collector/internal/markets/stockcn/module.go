package stockcn

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

type Module struct{ calendar *Calendar }

func New(calendar *Calendar) *Module { return &Module{calendar: calendar} }
func (*Module) Descriptor() markets.Descriptor {
	return markets.Descriptor{MarketID: "stock_cn", SpaceID: "stock_cn", AssetClass: "stock", Timezone: "Asia/Shanghai"}
}
func (*Module) Universe() markets.UniversePolicy {
	return UniversePolicy{AuthorityOrder: []marketdata.ProviderID{"tdx", "tencent", "ifeng"}, DelistingGraceDays: 5}
}
func (m *Module) Calendar() markets.CalendarPolicy { return m.calendar }
func (*Module) Symbols() markets.SymbolPolicy      { return SymbolPolicy{} }
func (*Module) Routing() markets.RoutingPolicy {
	return RoutingPolicy{ProviderPriority: []marketdata.ProviderID{"tdx", "tencent", "ifeng"}}
}
func (*Module) Quality() markets.QualityPolicy   { return QualityPolicy{PriceTolerance: "0.001"} }
func (*Module) Coverage() markets.CoveragePolicy { return CoveragePolicy{OverlapBuckets: 2} }

type UniversePolicy struct {
	AuthorityOrder     []marketdata.ProviderID
	DelistingGraceDays int
}
type SymbolPolicy struct{}
type RoutingPolicy struct{ ProviderPriority []marketdata.ProviderID }
type QualityPolicy struct{ PriceTolerance string }
type CoveragePolicy struct{ OverlapBuckets int }

func CanonicalSubject(code string) (string, error) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return "", fmt.Errorf("China security code must contain six digits")
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("invalid security code")
		}
	}
	exchange := "XSHE"
	switch code[0] {
	case '6':
		exchange = "XSHG"
	case '4', '8':
		exchange = "XBSE"
	}
	return code + "." + exchange, nil
}
func ProviderSymbol(provider, subject string) (string, error) {
	parts := strings.Split(subject, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid canonical subject %q", subject)
	}
	prefix := map[string]string{"XSHG": "sh", "XSHE": "sz", "XBSE": "bj"}[parts[1]]
	if prefix == "" {
		return "", fmt.Errorf("unknown exchange")
	}
	switch provider {
	case "ifeng", "tencent", "sina":
		return prefix + parts[0], nil
	case "tdx":
		return parts[0], nil
	default:
		return "", fmt.Errorf("unknown provider %q", provider)
	}
}
