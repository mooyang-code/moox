package marketfetch

// SymbolResolver is supplied by the composition root. The provider is explicit
// so one exchange's wire encoding cannot leak into another exchange's request.
type SymbolResolver func(provider, marketID, marketType, subjectID, configured string) (string, error)

func DefaultProviderSymbol(marketID, marketType, subjectID, configured string) (string, error) {
	return marketProviderSymbolForMarket(marketID, marketType, subjectID, configured)
}

func resolveProviderSymbol(resolver SymbolResolver, provider, marketID, marketType, subjectID, configured string) (string, error) {
	if resolver != nil {
		return resolver(provider, marketID, marketType, subjectID, configured)
	}
	return marketProviderSymbolForMarket(marketID, marketType, subjectID, configured)
}

type RuntimeResolvers struct {
	SourceID func(provider, instrumentType string) string
	Symbol   SymbolResolver
}
