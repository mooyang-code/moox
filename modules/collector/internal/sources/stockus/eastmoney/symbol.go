package eastmoney

import (
	"fmt"
	"strings"
)

var supportedUSExchanges = map[string]struct{}{
	"XNAS": {}, "XNYS": {}, "XASE": {},
}

func SecID(subjectID string) (string, error) {
	parts := strings.Split(strings.TrimSpace(subjectID), ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("unsupported US subject %q", subjectID)
	}
	if _, ok := supportedUSExchanges[strings.ToUpper(parts[1])]; !ok {
		return "", fmt.Errorf("unsupported US exchange %q", parts[1])
	}
	symbol := strings.ToUpper(strings.TrimSpace(parts[0]))
	if symbol == "" || len(symbol) > 16 || strings.ContainsAny(symbol, " ,/\\") {
		return "", fmt.Errorf("invalid US symbol %q", symbol)
	}
	return "105." + symbol, nil
}
