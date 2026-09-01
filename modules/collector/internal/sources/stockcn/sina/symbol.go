package sina

import (
	"fmt"
	"strings"
)

func normalizeSymbol(value string) (string, error) {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(value)), ".", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("sina: symbol %q must use EXCHANGE.CODE", value)
	}
	prefix := parts[0]
	if prefix != "SH" && prefix != "SZ" && prefix != "BJ" {
		return "", fmt.Errorf("sina: unsupported exchange %q", prefix)
	}
	code := parts[1]
	if len(code) != 6 {
		return "", fmt.Errorf("sina: symbol %q code must contain six digits", value)
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("sina: symbol %q code must contain six digits", value)
		}
	}
	return strings.ToLower(prefix + code), nil
}
