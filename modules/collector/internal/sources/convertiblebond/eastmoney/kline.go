package eastmoney

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	core "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
)

type Client struct{ *core.Client }

func NewClient(getter core.Getter) *Client {
	return &Client{Client: core.NewClient(core.Config{ProviderID: "eastmoney", SourceID: "convertible_bond_http", MarketID: "stock_cn", InstrumentType: "convertible_bond", Timezone: "Asia/Shanghai", Frequencies: []string{"1m", "5m", "15m", "30m", "60m", "1d"}, HistoryStart: "2017-01-01", SecID: SecID}, getter)}
}

func SecID(symbol string) (string, error) {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(symbol)), ".", 2)
	if len(parts) != 2 || (parts[0] != "SH" && parts[0] != "SZ") || parts[1] == "" {
		return "", fmt.Errorf("eastmoney convertible bond: symbol %q must use SH.CODE or SZ.CODE", symbol)
	}
	if parts[0] == "SH" {
		return "1." + parts[1], nil
	}
	return "0." + parts[1], nil
}

var _ marketdata.KlineFetcher = (*Client)(nil)
