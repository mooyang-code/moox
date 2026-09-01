package binance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
)

var (
	_ marketdata.MarketProvider    = (*MarketDataAdapter)(nil)
	_ marketdata.KlineFetcher      = (*MarketDataAdapter)(nil)
	_ marketdata.InstrumentFetcher = (*MarketDataAdapter)(nil)
)

type AdapterConfig struct {
	ProductType     marketdata.ProductType
	KlineCollector  *KlineCollector
	SymbolCollector *SymbolCollector
	Now             func() time.Time
}

// MarketDataAdapter exposes the existing Binance collectors through the common
// typed marketdata contracts. Protocol parsing, retry behavior, symbol
// filtering, and subject normalization stay owned by the existing collectors.
type MarketDataAdapter struct {
	defaultProductType  marketdata.ProductType
	klineCollector      *KlineCollector
	symbolCollector     *SymbolCollector
	now                 func() time.Time
	instrumentGuardOnce sync.Once
	instrumentGuard     *marketdata.FeedGuard
	instrumentGuardErr  error
}

func NewMarketDataAdapter(cfg AdapterConfig) *MarketDataAdapter {
	if cfg.ProductType == "" {
		cfg.ProductType = marketdata.ProductSpot
	}
	if cfg.KlineCollector == nil {
		cfg.KlineCollector = NewKlineCollector()
	}
	if cfg.SymbolCollector == nil {
		cfg.SymbolCollector = NewSymbolCollector()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MarketDataAdapter{
		defaultProductType: cfg.ProductType,
		klineCollector:     cfg.KlineCollector,
		symbolCollector:    cfg.SymbolCollector,
		now:                cfg.Now,
	}
}

func (*MarketDataAdapter) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ID:          "binance",
		DisplayName: "Binance",
		Hosts:       []string{"api.binance.com", "fapi.binance.com"},
	}
}

func (*MarketDataAdapter) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets:   []string{"crypto"},
		Exchanges: []string{"binance"},
		Frequencies: []string{
			string(marketdata.FrequencyMinute), string(marketdata.Frequency5Min),
			string(marketdata.Frequency15Min), string(marketdata.Frequency30Min),
			string(marketdata.FrequencyHour), string(marketdata.FrequencyDay),
			string(marketdata.FrequencyWeek),
		},
		CompleteOHLCV:     true,
		HasAmount:         true,
		MaxBarsPerRequest: 1000,
		SupportsBatch:     false,
		TimestampMode:     marketdata.TimestampModeOpen,
		History:           marketdata.KlineHistoryCapability{SupportsArbitraryRange: true},
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5,
			Burst:             5,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    5 * time.Second,
		},
	}
}

func (*MarketDataAdapter) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{
		Markets:      []string{"crypto"},
		Exchanges:    []string{"binance"},
		FullSnapshot: true,
		PageSize:     1,
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 1,
			Burst:             1,
			MaxConcurrent:     1,
			Cooldown:          time.Second,
			RequestTimeout:    10 * time.Second,
		},
	}
}

func (a *MarketDataAdapter) FetchKlines(ctx context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if err := validateBinanceRoute(req.MarketID, req.ExchangeID); err != nil {
		return nil, err
	}
	productType := req.ProductType
	if productType == "" {
		productType = a.defaultProductType
	}
	instType, err := InstTypeForMarket(string(productType))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", marketdata.ErrInvalidRequest, err)
	}
	if err := validateInstrumentType(productType, req.InstrumentType); err != nil {
		return nil, err
	}

	params := &sources.CollectParams{
		InstType:  instType,
		Symbol:    strings.TrimSpace(req.ProviderSymbol),
		SubjectID: strings.TrimSpace(req.SubjectID),
		Interval:  req.Frequency,
		DNSRoutes: marketDataDNSRoutes(req.DNSRoutes),
	}
	exchangeKlines, err := a.klineCollector.fetchKlinesOnce(ctx, params, &exchange.KlineRequest{
		Symbol:    params.Symbol,
		Interval:  req.Frequency,
		Limit:     req.Limit,
		StartTime: req.StartTime,
		EndTime:   req.EndTime,
	})
	if err != nil {
		return nil, classifyProviderError(err)
	}

	now := a.now().UTC()
	closed, _ := filterClosedKlines(convertExchangeKlines(exchangeKlines, params.Symbol, req.Frequency), now)
	if len(closed) == 0 {
		return nil, marketdata.ErrNoClosedBar
	}
	rows := make([]marketdata.NormalizedKline, 0, len(closed))
	for _, kline := range closed {
		row, err := normalizeMarketDataKline(req, kline, now)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (a *MarketDataAdapter) FetchInstrumentSnapshot(ctx context.Context, req marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	if err := validateBinanceRoute(req.MarketID, req.ExchangeID); err != nil {
		return marketdata.InstrumentSnapshot{}, err
	}
	instType, err := InstTypeForMarket(string(a.defaultProductType))
	if err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: %v", marketdata.ErrInvalidRequest, err)
	}
	marketID := strings.TrimSpace(string(req.MarketID))
	if marketID == "" {
		marketID = "crypto"
	}
	fetchedAt := req.SnapshotAt.UTC()
	if req.SnapshotAt.IsZero() {
		fetchedAt = a.now().UTC()
	}
	guardSpec := a.InstrumentSpec()
	a.instrumentGuardOnce.Do(func() {
		a.instrumentGuard, a.instrumentGuardErr = marketdata.NewFeedGuard(guardSpec.RateLimit, nil, nil)
	})
	if a.instrumentGuardErr != nil {
		return marketdata.InstrumentSnapshot{}, a.instrumentGuardErr
	}
	var symbols []*exchange.SymbolInfo
	err = a.instrumentGuard.Do(ctx, func(requestCtx context.Context) error {
		fetched, fetchErr := a.symbolCollector.fetchSymbols(requestCtx, &sources.CollectParams{
			SpaceID:   marketID,
			DatasetID: "instruments",
			InstType:  instType,
			DNSRoutes: marketDataDNSRoutes(req.DNSRoutes),
		})
		symbols = a.symbolCollector.filterSymbols(fetched)
		if len(symbols) == 0 && fetchErr == nil {
			return fmt.Errorf("Binance active USDT symbol snapshot is empty")
		}
		return fetchErr
	})
	if err != nil {
		return marketdata.InstrumentSnapshot{}, classifyProviderError(err)
	}

	instruments := make([]marketdata.Instrument, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol == nil {
			continue
		}
		instruments = append(instruments, marketdata.Instrument{
			SubjectID:       normalizedSubjectID(symbol, instType),
			CanonicalSymbol: symbol.Symbol,
			ProviderSymbol:  externalSymbol(symbol),
			Exchange:        "binance",
			Name:            strings.TrimSpace(symbol.BaseAsset) + "/" + strings.TrimSpace(symbol.QuoteAsset),
			Status:          symbol.Status,
			BaseAsset:       symbol.BaseAsset,
			QuoteAsset:      symbol.QuoteAsset,
			MinQty:          symbol.MinQty,
			MaxQty:          symbol.MaxQty,
			TickSize:        symbol.TickSize,
			LotSize:         symbol.LotSize,
		})
	}
	snapshot := marketdata.InstrumentSnapshot{
		SnapshotID:     fetchedAt.Format(time.RFC3339Nano),
		SourceProvider: "binance",
		MarketID:       marketID,
		FetchedAt:      fetchedAt,
		Complete:       true,
		PageCount:      1,
		ExchangeCounts: map[string]int{"binance": len(instruments)},
		Instruments:    instruments,
	}
	if err := marketdata.ValidateInstrumentSnapshot(snapshot); err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	return snapshot, nil
}

func validateBinanceRoute(marketID marketdata.MarketID, exchangeID marketdata.ExchangeID) error {
	if value := strings.TrimSpace(string(marketID)); value != "" && value != "crypto" {
		return fmt.Errorf("%w: market_id %q is unsupported", marketdata.ErrInvalidRequest, marketID)
	}
	if value := strings.TrimSpace(string(exchangeID)); value != "" && value != "binance" {
		return fmt.Errorf("%w: exchange_id %q is unsupported", marketdata.ErrInvalidRequest, exchangeID)
	}
	return nil
}

func validateInstrumentType(productType marketdata.ProductType, instrumentType marketdata.InstrumentType) error {
	if instrumentType == "" {
		return nil
	}
	want := marketdata.InstrumentSpot
	if productType == marketdata.ProductSwap {
		want = marketdata.InstrumentSwap
	}
	if instrumentType != want {
		return fmt.Errorf("%w: instrument_type %q does not match product_type %q", marketdata.ErrInvalidRequest, instrumentType, productType)
	}
	return nil
}

func normalizeMarketDataKline(req marketdata.KlineRequest, kline *market.Kline, fetchedAt time.Time) (marketdata.NormalizedKline, error) {
	if kline == nil {
		return marketdata.NormalizedKline{}, fmt.Errorf("%w: nil Binance kline", marketdata.ErrProtocol)
	}
	openValue, err := decimalToFloat64(kline.Open)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	highValue, err := decimalToFloat64(kline.High)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	lowValue, err := decimalToFloat64(kline.Low)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	closeValue, err := decimalToFloat64(kline.Close)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	volumeValue, err := decimalToFloat64(kline.Volume)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	amountValue, err := decimalToFloat64(kline.QuoteVolume)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	frequency, err := req.FrequencyValue()
	if err != nil {
		return marketdata.NormalizedKline{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	barDuration := frequency.Duration()
	barStart := normalizeBarStart(kline.OpenTime.UTC(), frequency)
	row := marketdata.NormalizedKline{
		SubjectID:         req.SubjectID,
		ProviderID:        "binance",
		ProviderSymbol:    req.ProviderSymbol,
		Frequency:         req.Frequency,
		BarStart:          barStart,
		BarEnd:            barStart.Add(barDuration),
		Open:              openValue,
		High:              highValue,
		Low:               lowValue,
		Close:             closeValue,
		VolumeShares:      volumeValue,
		AmountCNY:         amountValue,
		TradeCount:        kline.TradeCount,
		ProviderTimestamp: kline.CloseTime.UTC(),
		FetchedAt:         fetchedAt,
		RequestID:         req.RequestID,
	}
	if err := marketdata.ValidateNormalizedKline(row); err != nil {
		return marketdata.NormalizedKline{}, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	return row, nil
}

func normalizeBarStart(openTime time.Time, frequency marketdata.Frequency) time.Time {
	openTime = openTime.UTC()
	if frequency != marketdata.FrequencyWeek {
		return openTime.Truncate(frequency.Duration())
	}
	// Unix-duration truncation anchors weeks on Thursday. Binance's 1w
	// interval is conventionally a Monday 00:00 UTC bucket, so align it by
	// calendar date rather than by elapsed seconds.
	day := time.Date(openTime.Year(), openTime.Month(), openTime.Day(), 0, 0, 0, 0, time.UTC)
	daysSinceMonday := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -daysSinceMonday)
}

func marketDataDNSRoutes(routes map[string][]string) map[string]sources.DNSResolution {
	if len(routes) == 0 {
		return nil
	}
	result := make(map[string]sources.DNSResolution, len(routes))
	for host, ips := range routes {
		result[host] = sources.DNSResolution{IPs: append([]string(nil), ips...)}
	}
	return result
}

func decimalToFloat64(value interface{ Float64() (float64, error) }) (float64, error) {
	floatValue, err := value.Float64()
	if err != nil {
		return 0, fmt.Errorf("%w: %v", marketdata.ErrProtocol, err)
	}
	return floatValue, nil
}

func classifyProviderError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var statusErr *httpclient.StatusError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("%w: %v", marketdata.ErrRateLimited, err)
		}
		return fmt.Errorf("%w: %v", marketdata.ErrHTTPStatus, err)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	return err
}
