package eastmoney

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

const defaultDomain = "push2his.eastmoney.com"

type Getter interface {
	Get(context.Context, string, string, url.Values, interface{}) error
}

type Client struct {
	HTTP   Getter
	Domain string
	Now    func() time.Time
}

func NewClient(httpGetter Getter) *Client {
	if httpGetter == nil {
		httpGetter = httpclient.NewHTTPClient()
	}
	return &Client{HTTP: httpGetter, Domain: defaultDomain}
}

func (c *Client) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ProviderID: "eastmoney", SourceID: "stock_cn_http", ProtocolVariant: marketdata.ProtocolHTTP, Transport: "https", Host: c.domain(), Port: 443, Markets: []string{"stock_cn"}, InstrumentTypes: []string{"equity"}, Frequencies: []string{"1m", "5m", "15m", "30m", "60m", "1d", "1w", "1M"}}
}

func (c *Client) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{MarketID: "stock_cn", InstrumentType: "equity", Frequencies: c.Descriptor().Frequencies, CompleteOHLCV: true, HasAmount: true, VolumeUnit: "share", AmountUnit: "cny", TimestampMode: "start-label", SupportsRange: true, MaxBarsPerRequest: 1000, RequestTimeoutSeconds: 5, HistoryStart: "1990-01-01"}
}

func (c *Client) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if c == nil || c.HTTP == nil {
		return nil, fmt.Errorf("eastmoney: client is not initialized")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.MarketID != "stock_cn" || request.InstrumentType != "equity" {
		return nil, fmt.Errorf("eastmoney: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	if !c.KlineSpec().SupportsFrequency(request.Frequency) {
		return nil, fmt.Errorf("eastmoney: unsupported frequency %q", request.Frequency)
	}
	secid, err := SecID(request.ProviderSymbol)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("secid", secid)
	query.Set("klt", klineType(request.Frequency))
	query.Set("fqt", "0")
	query.Set("beg", beginDate(request.StartTime))
	query.Set("end", endDate(request.EndTime))
	query.Set("lmt", strconv.Itoa(limit(request.Limit)))
	query.Set("fields1", "f1,f2,f3,f4,f5,f6")
	query.Set("fields2", "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61")
	var payload response
	if err := c.HTTP.Get(ctx, c.domain(), "/api/qt/stock/kline/get", query, &payload); err != nil {
		return nil, fmt.Errorf("eastmoney: fetch %s: %w", request.ProviderSymbol, err)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	return parseKlines(payload, request, now())
}

func (c *Client) domain() string {
	if value := strings.TrimSpace(c.Domain); value != "" {
		return value
	}
	return defaultDomain
}

func klineType(frequency string) string {
	switch strings.TrimSpace(frequency) {
	case "1d":
		return "101"
	case "1w":
		return "102"
	case "1M":
		return "103"
	case "1m":
		return "1"
	case "5m":
		return "5"
	case "15m":
		return "15"
	case "30m":
		return "30"
	case "60m":
		return "60"
	default:
		return "0"
	}
}

func beginDate(value time.Time) string {
	if value.IsZero() {
		return "0"
	}
	return value.In(time.FixedZone("CST", 8*60*60)).Format("20060102")
}

func endDate(value time.Time) string {
	if value.IsZero() {
		return "20500101"
	}
	return value.In(time.FixedZone("CST", 8*60*60)).Format("20060102")
}

func limit(value int) int {
	if value <= 0 || value > 1000 {
		return 1000
	}
	return value
}
