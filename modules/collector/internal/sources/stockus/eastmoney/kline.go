package eastmoney

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	core "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
)

type Client struct{ *core.Client }

func NewClient(getter core.Getter) *Client {
	return &Client{Client: core.NewClient(core.Config{ProviderID: "eastmoney", SourceID: "stock_us_http", MarketID: "stock_us", InstrumentType: "equity", Timezone: "America/New_York", Frequencies: []string{"1m", "5m", "15m", "30m", "60m", "1d", "1w", "1M"}, VolumeUnit: "share", AmountUnit: "usd", HistoryStart: "2000-01-01", SecID: SecID}, getter)}
}

func SecID(symbol string) (string, error) {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(symbol)), ".", 2)
	if len(parts) != 2 || parts[0] != "US" || parts[1] == "" {
		return "", fmt.Errorf("eastmoney us: symbol %q must use US.CODE", symbol)
	}
	return "105." + parts[1], nil
}

var _ marketdata.KlineFetcher = (*Client)(nil)
