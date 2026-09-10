package binance

import (
	"fmt"
	"strings"
)

// ProviderSymbol maps the catalog subject to the Binance wire symbol.
func ProviderSymbol(subjectID, configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" && !strings.Contains(configured, "-") {
		return configured, nil
	}
	// Binance subjects are canonicalized as BASE-QUOTE-SPOT/SWAP. The
	// symbol catalog may contain the subject row before the instrument
	// snapshot has populated external_symbol, so derive the wire symbol
	// instead of dropping the entire shard during reconciliation.
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(subjectID)), "-")
	if len(parts) >= 3 && (parts[len(parts)-1] == "SPOT" || parts[len(parts)-1] == "SWAP") {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		return "", fmt.Errorf("subject %s has no Binance symbol mapping", subjectID)
	}
	for _, part := range parts {
		if !isBinanceSymbolPart(part) {
			return "", fmt.Errorf("subject %s has no Binance symbol mapping", subjectID)
		}
	}
	return strings.Join(parts, ""), nil
}

func isBinanceSymbolPart(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}
