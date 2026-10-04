package tdx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
)

type Config struct {
	Host              string
	Port              int
	Timeout           time.Duration
	Dial              tdxwire.DialFunc
	Now               func() time.Time
	RateLimit         marketdata.RateLimitPolicy
	MaxBarsPerRequest int
}

type Provider struct {
	host              string
	port              int
	timeout           time.Duration
	dial              tdxwire.DialFunc
	now               func() time.Time
	rateLimit         marketdata.RateLimitPolicy
	maxBarsPerRequest int
}

func New(cfg Config) *Provider {
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = "jstdx.gtjas.com"
	}
	if cfg.Port <= 0 {
		cfg.Port = 7709
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RateLimit.RequestsPerSecond <= 0 {
		cfg.RateLimit = marketdata.RateLimitPolicy{
			RequestsPerSecond: 5,
			Burst:             2,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    cfg.Timeout,
		}
	}
	if cfg.MaxBarsPerRequest <= 0 {
		cfg.MaxBarsPerRequest = 800
	}
	return &Provider{
		host:              strings.TrimSpace(cfg.Host),
		port:              cfg.Port,
		timeout:           cfg.Timeout,
		dial:              cfg.Dial,
		now:               cfg.Now,
		rateLimit:         cfg.RateLimit,
		maxBarsPerRequest: cfg.MaxBarsPerRequest,
	}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ID:              "tdx",
		SourceID:        "normal_7709",
		DisplayName:     "TDX normal 7709",
		Hosts:           []string{p.host},
		ProtocolVariant: "tdx_normal",
		Transport:       "tcp",
		Port:            p.port,
		Status:          marketdata.SourceEnabled,
	}
}

func (p *Provider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:           []string{"stockcn"},
		Exchanges:         []string{"XSHG", "XSHE", "XBSE"},
		Frequencies:       []string{"1m", "5m", "15m", "30m", "60m", "1d", "1w"},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: p.maxBarsPerRequest,
		TimestampMode:     marketdata.TimestampModeOpen,
		History:           marketdata.KlineHistoryCapability{MaxLookback: 365 * 24 * time.Hour},
		RateLimit:         p.rateLimit,
	}
}

func (p *Provider) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if req.SourceID != "" && !strings.EqualFold(strings.TrimSpace(req.SourceID), "normal_7709") {
		return nil, fmt.Errorf("%w: tdx source_id %q is not normal_7709", marketdata.ErrUnsupportedSymbol, req.SourceID)
	}
	category, err := categoryForFrequency(req.Frequency)
	if err != nil {
		return nil, err
	}
	market, err := marketForExchange(req.ExchangeID)
	if err != nil {
		return nil, err
	}
	providerSymbol, err := commonsrc.ProviderSymbol(req.SubjectID)
	if err != nil {
		return nil, err
	}
	code := providerSymbol[2:]
	count := req.Limit
	if count > p.maxBarsPerRequest {
		count = p.maxBarsPerRequest
	}
	if count <= 0 {
		return nil, fmt.Errorf("%w: tdx bar limit must be positive", marketdata.ErrInvalidRequest)
	}
	client, err := tdxwire.NewClient(tdxwire.ClientOptions{
		Host: p.host, Port: p.port, Variant: tdxwire.ProtocolNormal,
		Timeout: p.timeout, Dial: p.dial,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: create tdx client: %v", marketdata.ErrTCP, err)
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTCP, err)
	}
	defer client.Close()
	normal := &tdxwire.NormalClient{Client: client}
	bars, err := normal.SecurityBars(ctx, market, code, category, 0, count, false)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: fetch %s: %v", marketdata.ErrProtocol, providerSymbol, err)
	}
	fetchedAt := p.now().UTC()
	rows, err := normalizeBars(req.SubjectID, providerSymbol, req.Frequency, bars, fetchedAt, req.RequestID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func categoryForFrequency(frequency string) (tdxwire.KlineCategory, error) {
	raw := strings.TrimSpace(frequency)
	if raw == "1M" {
		return tdxwire.CategoryMonth, nil
	}
	switch strings.ToLower(raw) {
	case "1m":
		return tdxwire.Category1Min, nil
	case "5m":
		return tdxwire.Category5Min, nil
	case "15m":
		return tdxwire.Category15Min, nil
	case "30m":
		return tdxwire.Category30Min, nil
	case "60m", "1h":
		return tdxwire.Category60Min, nil
	case "1d":
		return tdxwire.CategoryDay, nil
	case "1w":
		return tdxwire.CategoryWeek, nil
	default:
		return 0, fmt.Errorf("%w: tdx frequency %q", marketdata.ErrUnsupportedFrequency, frequency)
	}
}

func marketForExchange(exchange marketdata.ExchangeID) (tdxwire.Market, error) {
	switch strings.ToUpper(strings.TrimSpace(string(exchange))) {
	case "XSHG":
		return tdxwire.MarketSH, nil
	case "XSHE":
		return tdxwire.MarketSZ, nil
	case "XBSE":
		return tdxwire.MarketBJ, nil
	default:
		return 0, fmt.Errorf("%w: tdx exchange %q", marketdata.ErrUnsupportedSymbol, exchange)
	}
}

func normalizeBars(subjectID, providerSymbol, frequency string, bars []tdxwire.Bar, fetchedAt time.Time, requestID string) ([]marketdata.NormalizedKline, error) {
	parsedFrequency, err := marketdata.ParseFrequency(frequency)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrUnsupportedFrequency, err)
	}
	if parsedFrequency.Duration() <= 0 && parsedFrequency != marketdata.FrequencyMonth {
		return nil, fmt.Errorf("%w: tdx frequency %q has no duration", marketdata.ErrUnsupportedFrequency, frequency)
	}
	rows := make([]marketdata.NormalizedKline, 0, len(bars))
	var previous time.Time
	for index, bar := range bars {
		barStart := bar.Time
		if parsedFrequency == marketdata.FrequencyDay || parsedFrequency == marketdata.FrequencyWeek || parsedFrequency == marketdata.FrequencyMonth {
			local := bar.Time.In(time.FixedZone("Asia/Shanghai", 8*60*60))
			if parsedFrequency == marketdata.FrequencyMonth {
				barStart = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
			} else {
				barStart = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
			}
		}
		barStart = barStart.UTC()
		if !previous.IsZero() && !barStart.After(previous) {
			return nil, fmt.Errorf("%w: tdx bars are not strictly increasing at index %d", marketdata.ErrProtocol, index)
		}
		row := marketdata.NormalizedKline{
			SubjectID:         subjectID,
			ProviderID:        "tdx",
			SourceID:          "normal_7709",
			ProviderSymbol:    providerSymbol,
			Frequency:         string(parsedFrequency),
			BarStart:          barStart,
			BarEnd:            parsedFrequency.BarEnd(barStart),
			Open:              bar.Open,
			High:              bar.High,
			Low:               bar.Low,
			Close:             bar.Close,
			VolumeShares:      bar.Volume,
			AmountCNY:         bar.Amount,
			ProviderTimestamp: bar.Time.UTC(),
			FetchedAt:         fetchedAt,
			RequestID:         requestID,
		}
		if err := marketdata.ValidateNormalizedKline(row); err != nil {
			return nil, fmt.Errorf("tdx bar %d: %w", index, err)
		}
		rows = append(rows, row)
		previous = barStart
	}
	return rows, nil
}
