// Package crypto defines the durable identity rules for crypto subjects.
package crypto

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// SubjectID builds the shared spot and swap identity from structured exchange assets.
func SubjectID(base, quote string) (string, error) {
	base = strings.ToUpper(strings.TrimSpace(base))
	quote = strings.ToUpper(strings.TrimSpace(quote))
	if !validAsset(base) || !validAsset(quote) {
		return "", fmt.Errorf("%w: invalid crypto asset pair %q/%q", marketdata.ErrUnsupportedSymbol, base, quote)
	}
	return base + "-" + quote, nil
}

func validAsset(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
