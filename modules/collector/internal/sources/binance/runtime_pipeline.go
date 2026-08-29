package binance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// RuntimePipeline preserves the established crypto Storage contract while all
// upstream reads run through the registered typed provider interfaces.
type RuntimePipeline struct {
	provider    marketdata.MarketProvider
	klines      marketdata.KlineFetcher
	instruments marketdata.InstrumentFetcher
	productType marketdata.ProductType
}

func NewRuntimePipeline(productType marketdata.ProductType, klines *KlineCollector, symbols *SymbolCollector) (*RuntimePipeline, error) {
	provider := NewMarketDataAdapter(AdapterConfig{ProductType: productType, KlineCollector: klines, SymbolCollector: symbols})
	registry := marketdata.NewRegistry()
	if err := registry.Register(provider); err != nil {
		return nil, fmt.Errorf("register Binance market provider: %w", err)
	}
	klineFetcher, err := registry.KlineFetcher("binance")
	if err != nil {
		return nil, fmt.Errorf("resolve Binance kline fetcher: %w", err)
	}
	instrumentFetcher, err := registry.InstrumentFetcher("binance")
	if err != nil {
		return nil, fmt.Errorf("resolve Binance instrument fetcher: %w", err)
	}
	return &RuntimePipeline{provider: provider, klines: klineFetcher, instruments: instrumentFetcher, productType: productType}, nil
}

func (p *RuntimePipeline) Provider() marketdata.MarketProvider             { return p.provider }
func (p *RuntimePipeline) KlineFetcher() marketdata.KlineFetcher           { return p.klines }
func (p *RuntimePipeline) InstrumentFetcher() marketdata.InstrumentFetcher { return p.instruments }

func (p *RuntimePipeline) FetchRealtimeRows(ctx context.Context, params *sources.CollectParams, limit int) ([]*storagepb.RowFieldUpsert, time.Time, error) {
	return p.fetchKlineRows(ctx, params, time.Time{}, limit)
}

func (p *RuntimePipeline) FetchCatchupRows(ctx context.Context, params *sources.CollectParams, start time.Time, limit int) ([]*storagepb.RowFieldUpsert, time.Time, error) {
	if start.IsZero() {
		return nil, time.Time{}, fmt.Errorf("catchup start_time is required")
	}
	return p.fetchKlineRows(ctx, params, start.UTC(), limit)
}

func (p *RuntimePipeline) fetchKlineRows(ctx context.Context, params *sources.CollectParams, start time.Time, limit int) ([]*storagepb.RowFieldUpsert, time.Time, error) {
	if params == nil {
		return nil, time.Time{}, fmt.Errorf("K-line collection params are required")
	}
	if limit <= 0 || limit > 1000 {
		return nil, time.Time{}, fmt.Errorf("kline limit must be between 1 and 1000")
	}
	rows, err := p.klines.FetchKlines(ctx, marketdata.KlineRequest{
		MarketID: "crypto", ExchangeID: "binance", ProductType: p.productType,
		SubjectID: strings.TrimSpace(params.SubjectID), ProviderSymbol: strings.TrimSpace(params.Symbol),
		Frequency: params.Interval, Limit: limit, StartTime: start, RequestID: "marketfetch-runtime",
		DNSRoutes: flattenDNSRoutes(params.DNSRoutes),
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	result := make([]*storagepb.RowFieldUpsert, 0, len(rows))
	var watermark time.Time
	for _, row := range rows {
		result = append(result, &storagepb.RowFieldUpsert{
			Key:    &storagepb.RowKey{SpaceId: params.SpaceID, DatasetId: params.DatasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: row.SubjectID, Freq: row.Frequency, DataTime: row.BarStart.UTC().Format(time.RFC3339Nano), SeriesTag: binanceSeriesTag}}},
			Fields: []*storagepb.FieldValue{doubleField("open", row.Open), doubleField("high", row.High), doubleField("low", row.Low), doubleField("close", row.Close), doubleField("volume", row.VolumeShares), doubleField("quote_volume", row.AmountCNY), intField("trade_num", row.TradeCount)},
		})
		if row.BarStart.After(watermark) {
			watermark = row.BarStart.UTC()
		}
	}
	return result, watermark, nil
}

func (p *RuntimePipeline) FetchSymbolSnapshot(ctx context.Context, params *sources.CollectParams) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, string, error) {
	if params == nil {
		return nil, nil, "", fmt.Errorf("symbol collection params are required")
	}
	snapshot, err := p.instruments.FetchInstrumentSnapshot(ctx, marketdata.InstrumentRequest{MarketID: "crypto", ExchangeID: "binance", SnapshotAt: time.Now().UTC(), RequestID: "marketfetch-runtime", DNSRoutes: flattenDNSRoutes(params.DNSRoutes)})
	if err != nil {
		return nil, nil, "", err
	}
	symbols := make([]*exchange.SymbolInfo, 0, len(snapshot.Instruments))
	for _, instrument := range snapshot.Instruments {
		symbols = append(symbols, &exchange.SymbolInfo{Symbol: instrument.CanonicalSymbol, BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset, Status: instrument.Status, MinQty: instrument.MinQty, MaxQty: instrument.MaxQty, TickSize: instrument.TickSize, LotSize: instrument.LotSize})
	}
	rows, err := buildSymbolRecordRows(symbols, params.SpaceID, params.DatasetID, params.InstType)
	if err != nil {
		return nil, nil, "", err
	}
	for _, row := range rows {
		if record := row.GetKey().GetRecord(); record != nil {
			record.Version = snapshot.SnapshotID
		}
	}
	return rows, symbols, snapshot.SnapshotID, nil
}

func flattenDNSRoutes(routes map[string]sources.DNSResolution) map[string][]string {
	if len(routes) == 0 {
		return nil
	}
	result := make(map[string][]string, len(routes))
	for host, route := range routes {
		result[host] = append([]string(nil), route.IPs...)
	}
	return result
}
