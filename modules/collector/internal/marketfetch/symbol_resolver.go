package marketfetch

// SymbolResolver is supplied by the composition root. The provider is explicit
// so one exchange's wire encoding cannot leak into another exchange's request.
// Provider symbols are derived solely from the canonical SubjectID.
type SymbolResolver func(provider, marketID, marketType, subjectID string) (string, error)

// SourceIDResolver 返回（市场，数据源，品种）在行情源目录里登记的 source_id；没登记时为空。由组合根提供。
type SourceIDResolver func(marketID, provider, instrumentType string) string

// DefaultProviderSymbol 按市场的标的规则把标的 ID 换成传给数据源的代码；marketType 为空时用市场的默认品种。
func DefaultProviderSymbol(marketID, marketType, subjectID string) (string, error) {
	instrumentType, err := defaultInstrumentType(marketID, marketType)
	if err != nil {
		return "", err
	}
	subject, err := parseSubject(marketID, instrumentType, subjectID)
	if err != nil {
		return "", err
	}
	return subject.Code, nil
}

func resolveProviderSymbol(resolver SymbolResolver, provider, marketID, marketType, subjectID string) (string, error) {
	if resolver != nil {
		return resolver(provider, marketID, marketType, subjectID)
	}
	return DefaultProviderSymbol(marketID, marketType, subjectID)
}
