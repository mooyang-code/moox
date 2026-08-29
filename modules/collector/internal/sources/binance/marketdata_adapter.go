package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	binanceclient "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
)

type AdapterConfig struct {
	ProductType marketdata.ProductType
	SpotBaseURL string
	SwapBaseURL string
	HTTPClient  *http.Client
	Now         func() time.Time
}

type MarketDataAdapter struct {
	productType marketdata.ProductType
	baseURL     string
	client      *http.Client
	now         func() time.Time
}

func NewMarketDataAdapter(cfg AdapterConfig) *MarketDataAdapter {
	if cfg.ProductType == "" {
		cfg.ProductType = marketdata.ProductSpot
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	baseURL := strings.TrimRight(cfg.SpotBaseURL, "/")
	if cfg.ProductType == marketdata.ProductSwap {
		baseURL = strings.TrimRight(cfg.SwapBaseURL, "/")
	}
	if baseURL == "" {
		if cfg.ProductType == marketdata.ProductSwap {
			baseURL = "https://fapi.binance.com"
		} else {
			baseURL = "https://api.binance.com"
		}
	}
	return &MarketDataAdapter{
		productType: cfg.ProductType,
		baseURL:     baseURL,
		client:      cfg.HTTPClient,
		now:         cfg.Now,
	}
}

func (*MarketDataAdapter) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ID:          "binance",
		DisplayName: "Binance",
		Hosts:       []string{"api.binance.com", "fapi.binance.com"},
	}
}

func (a *MarketDataAdapter) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"crypto"},
		Exchanges:         []string{"binance"},
		Frequencies:       []string{string(marketdata.FrequencyMinute)},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: 1000,
		SupportsBatch:     false,
		TimestampMode:     marketdata.TimestampModeOpen,
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5,
			Burst:             5,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    5 * time.Second,
		},
	}
}

func (a *MarketDataAdapter) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	frequency, _ := req.FrequencyValue()
	if frequency != marketdata.FrequencyMinute {
		return nil, fmt.Errorf("%w: binance adapter only supports 1m", marketdata.ErrUnsupportedFrequency)
	}
	klines, err := a.fetchKlines(ctx, req)
	if err != nil {
		return nil, err
	}
	rows := make([]marketdata.NormalizedKline, 0, len(klines))
	now := a.now().UTC()
	for _, kline := range klines {
		row, err := a.normalizeKline(req, kline, now)
		if err != nil {
			return nil, err
		}
		if !now.Before(row.BarEnd) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	return rows, nil
}

func (a *MarketDataAdapter) fetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]binanceclient.CandleStick, error) {
	query := url.Values{
		"symbol":   {binanceclient.FormatSymbol(strings.TrimSpace(req.ProviderSymbol))},
		"interval": {req.Frequency},
		"limit":    {fmt.Sprintf("%d", req.Limit)},
	}
	if !req.StartTime.IsZero() {
		query.Set("startTime", fmt.Sprintf("%d", req.StartTime.UTC().UnixMilli()))
	}
	if !req.EndTime.IsZero() {
		query.Set("endTime", fmt.Sprintf("%d", req.EndTime.UTC().UnixMilli()))
	}
	path := "/api/v3/klines"
	switch a.productType {
	case marketdata.ProductSpot:
	case marketdata.ProductSwap:
		path = "/fapi/v1/klines"
	default:
		return nil, fmt.Errorf("%w: unsupported product type %q", marketdata.ErrInvalidRequest, a.productType)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "moox-collector/1.0")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, marketdata.ErrRateLimited
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var payload []binanceclient.CandleStick
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	return payload, nil
}

func (a *MarketDataAdapter) normalizeKline(req marketdata.KlineRequest, kline binanceclient.CandleStick, fetchedAt time.Time) (marketdata.NormalizedKline, error) {
	openValue, err := decimalStringToFloat64(kline.Open)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	highValue, err := decimalStringToFloat64(kline.High)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	lowValue, err := decimalStringToFloat64(kline.Low)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	closeValue, err := decimalStringToFloat64(kline.Close)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	volumeValue, err := decimalStringToFloat64(kline.Volume)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	amountValue, err := decimalStringToFloat64(kline.QuoteVolume)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	barStart := time.UnixMilli(kline.OpenTime).UTC().Truncate(time.Minute)
	row := marketdata.NormalizedKline{
		SubjectID:         req.SubjectID,
		ProviderID:        "binance",
		ProviderSymbol:    req.ProviderSymbol,
		Frequency:         req.Frequency,
		BarStart:          barStart,
		BarEnd:            barStart.Add(time.Minute),
		Open:              openValue,
		High:              highValue,
		Low:               lowValue,
		Close:             closeValue,
		VolumeShares:      volumeValue,
		AmountCNY:         amountValue,
		ProviderTimestamp: time.UnixMilli(kline.CloseTime).UTC(),
		FetchedAt:         fetchedAt,
		RequestID:         req.RequestID,
	}
	if err := marketdata.ValidateNormalizedKline(row); err != nil {
		return marketdata.NormalizedKline{}, err
	}
	return row, nil
}

func decimalStringToFloat64(value string) (float64, error) {
	return decimalToFloat64(common.NewDecimal(value))
}

func decimalToFloat64(value common.Decimal) (float64, error) {
	floatValue, err := value.Float64()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	return floatValue, nil
}
