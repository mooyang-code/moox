package marketfetch

// SymbolResolver is supplied by the composition root. The provider is explicit
// so one exchange's wire encoding cannot leak into another exchange's request.
// Provider symbols are derived solely from the canonical SubjectID.
type SymbolResolver func(provider, marketID, marketType, subjectID string) (string, error)

func DefaultProviderSymbol(marketID, marketType, subjectID string) (string, error) {
	return marketProviderSymbolForMarket(marketID, marketType, subjectID)
}

func resolveProviderSymbol(resolver SymbolResolver, provider, marketID, marketType, subjectID string) (string, error) {
	if resolver != nil {
		return resolver(provider, marketID, marketType, subjectID)
	}
	return marketProviderSymbolForMarket(marketID, marketType, subjectID)
}

type RuntimeResolvers struct {
	SourceID func(provider, instrumentType string) string
	Symbol   SymbolResolver
}
