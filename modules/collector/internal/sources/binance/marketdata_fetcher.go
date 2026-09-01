package binance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/mooyang-code/moox/packages/routeprobe"
)

// MarketDataFetcher is the canonical KlineFetcher adapter for the existing
// Binance HTTP clients. It has no Storage dependency and is selected by the
// same SourceKey as stock providers.
type MarketDataFetcher struct {
	Client   *binanceapi.Client
	Spot     *binanceapi.SpotAPI
	Swap     *binanceapi.SwapAPI
	InstType string
	Routes   marketdata.RouteIPProvider
	Now      func() time.Time
}

func NewMarketDataFetcher(instType string) *MarketDataFetcher {
	client := newConfiguredClient()
	return &MarketDataFetcher{Client: client, Spot: binanceapi.NewSpotAPI(client), Swap: binanceapi.NewSwapAPI(client), InstType: strings.ToUpper(strings.TrimSpace(instType))}
}

func (f *MarketDataFetcher) Descriptor() marketdata.ProviderDescriptor {
	instType := "spot"
	if f != nil && f.InstType == InstTypeSWAP {
		instType = "swap"
	}
	sourceID := "spot_http"
	if f != nil && f.InstType == InstTypeSWAP {
		sourceID = "swap_http"
	}
	return marketdata.ProviderDescriptor{ProviderID: "binance", SourceID: sourceID, ProtocolVariant: marketdata.ProtocolHTTP, Transport: "https", Port: 443, Markets: []string{"crypto"}, InstrumentTypes: []string{instType}, Frequencies: []string{"1m", "3m", "5m", "15m", "30m", "1h", "1H", "2h", "4h", "6h", "12h", "1d", "1w", "1M"}}
}

func (f *MarketDataFetcher) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{MarketID: "crypto", InstrumentType: strings.ToLower(f.InstType), Frequencies: f.Descriptor().Frequencies, CompleteOHLCV: true, HasAmount: true, VolumeUnit: "base", AmountUnit: "quote", TimestampMode: "start-label", SupportsRange: true, MaxBarsPerRequest: 1000, SupportsAdjustment: false, RequestTimeoutSeconds: 5}
}

func (f *MarketDataFetcher) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if f == nil {
		return nil, fmt.Errorf("binance: market data fetcher is nil")
	}
	if request.MarketID != "crypto" || request.InstrumentType != strings.ToLower(f.InstType) {
		return nil, fmt.Errorf("binance: unsupported market/instrument %s/%s", request.MarketID, request.InstrumentType)
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	fetcher := f.Spot
	sourceID := "spot_http"
	if f.InstType == InstTypeSWAP {
		fetcher = nil
		sourceID = "swap_http"
	}
	var raw []*exchange.Kline
	var err error
	convertedRequest := &exchange.KlineRequest{Symbol: request.ProviderSymbol, Interval: binanceInterval(request.Frequency), Limit: request.Limit, StartTime: request.StartTime, EndTime: request.EndTime}
	if f.InstType == InstTypeSWAP {
		if f.Swap == nil {
			return nil, fmt.Errorf("binance: swap client is not initialized")
		}
		ips, routeErr := f.routeIPs(ctx, sourceID)
		if routeErr != nil {
			return nil, routeErr
		}
		raw, err = f.Swap.GetKlineWithIPs(ctx, convertedRequest, ips)
	} else {
		if fetcher == nil {
			return nil, fmt.Errorf("binance: spot client is not initialized")
		}
		ips, routeErr := f.routeIPs(ctx, sourceID)
		if routeErr != nil {
			return nil, routeErr
		}
		raw, err = fetcher.GetKlineWithIPs(ctx, convertedRequest, ips)
	}
	if err != nil {
		return nil, err
	}
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	result := make([]marketdata.NormalizedKline, 0, len(raw))
	for _, item := range raw {
		if item == nil {
			continue
		}
		result = append(result, marketdata.NormalizedKline{SubjectID: request.SubjectID, ProviderID: "binance", SourceID: sourceID, ProviderSymbol: request.ProviderSymbol, Frequency: request.Frequency, BarStart: item.OpenTime.UTC(), BarEnd: item.CloseTime.UTC().Add(time.Millisecond), Open: item.Open, High: item.High, Low: item.Low, Close: item.Close, Volume: item.Volume, Amount: marketdata.OptionalDecimal{Value: item.QuoteVolume, Valid: true}, VolumeUnit: "base", AmountUnit: "quote", ProviderTime: item.OpenTime.UTC(), FetchedAt: now().UTC()})
	}
	return result, nil
}

func binanceInterval(frequency string) string {
	switch strings.TrimSpace(frequency) {
	case "1H":
		return "1h"
	case "1D":
		return "1d"
	case "1W":
		return "1w"
	default:
		return strings.TrimSpace(frequency)
	}
}

func (f *MarketDataFetcher) routeIPs(ctx context.Context, sourceID string) ([]string, error) {
	if f == nil || f.Routes == nil {
		return nil, nil
	}
	if f.Client == nil {
		return nil, fmt.Errorf("binance: route-aware fetcher has no client")
	}
	host := f.Client.SpotDomain()
	if sourceID == "swap_http" {
		host = f.Client.SwapDomain()
	}
	return f.Routes.SelectRouteIPs(ctx, routeprobe.SourceKey{ProviderID: "binance", SourceID: sourceID}, routeprobe.TransportHTTPS, host, 443)
}
