package eastmoney

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	core "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
)

type Client struct{ *core.Client }

func NewClient(getter core.Getter) *Client {
	return &Client{Client: core.NewClient(core.Config{ProviderID: "eastmoney", SourceID: "stock_hk_http", MarketID: "stock_hk", InstrumentType: "equity", Timezone: "Asia/Hong_Kong", Frequencies: []string{"1m", "5m", "15m", "30m", "60m", "1d", "1w", "1M"}, VolumeUnit: "share", AmountUnit: "hkd", HistoryStart: "2000-01-01", SecID: SecID}, getter)}
}

func SecID(symbol string) (string, error) {
	parts := strings.SplitN(strings.ToUpper(strings.TrimSpace(symbol)), ".", 2)
	if len(parts) != 2 || parts[0] != "HK" || parts[1] == "" {
		return "", fmt.Errorf("eastmoney hk: symbol %q must use HK.CODE", symbol)
	}
	return "116." + parts[1], nil
}

var _ marketdata.KlineFetcher = (*Client)(nil)
