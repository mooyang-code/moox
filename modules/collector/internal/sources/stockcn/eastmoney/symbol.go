package eastmoney

import (
	"fmt"
	"regexp"
	"strings"
)

var providerSymbolPattern = regexp.MustCompile(`^(SH|SZ|BJ)\.[0-9A-Za-z]{1,8}$`)

func SecID(symbol string) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if !providerSymbolPattern.MatchString(symbol) {
		return "", fmt.Errorf("eastmoney: symbol %q must use EXCHANGE.CODE", symbol)
	}
	parts := strings.SplitN(symbol, ".", 2)
	market := ""
	switch parts[0] {
	case "SH":
		market = "1"
	case "SZ", "BJ":
		market = "0"
	}
	return market + "." + parts[1], nil
}
