package binance

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/crypto"
)

// ToSymbol maps the suffix-free BASE-QUOTE subject to the Binance wire symbol.
func ToSymbol(subjectID string) (string, error) {
	value := strings.ToUpper(strings.TrimSpace(subjectID))
	value = strings.TrimSuffix(strings.TrimSuffix(value, "-SPOT"), "-SWAP")
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return "", fmt.Errorf("%w: subject %s is not a BASE-QUOTE pair", marketdata.ErrUnsupportedSymbol, subjectID)
	}
	if _, err := crypto.SubjectID(parts[0], parts[1]); err != nil {
		return "", err
	}
	return parts[0] + parts[1], nil
}

// ToSubjectID converts only structured exchange assets into a durable subject ID.
func ToSubjectID(symbol *binanceapi.SymbolInfo) (string, error) {
	if symbol == nil {
		return "", fmt.Errorf("%w: nil symbol", marketdata.ErrUnsupportedSymbol)
	}
	return crypto.SubjectID(symbol.BaseAsset, symbol.QuoteAsset)
}
