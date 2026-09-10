package marketwiring

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type capturedStorage struct {
	timerHandlerStorage
	rows []*storagepb.RowFieldUpsert
}

func (s *capturedStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.rows = append(s.rows, rows...)
	return nil
}

func TestComposedHandlerUsesProductEndpointAndPersistsSource(t *testing.T) {
	for _, product := range []string{"spot", "swap"} {
		for _, frequency := range []string{"1m", "1H"} {
			for _, mode := range []string{"invoke", "timer"} {
				t.Run(product+"/"+frequency+"/"+mode, func(t *testing.T) {
					now := time.Now().UTC().Truncate(time.Hour)
					start := now.Add(-time.Hour)
					barDuration := time.Minute
					if frequency == "1H" {
						barDuration = time.Hour
					}
					expectedPath := "/api/v3/klines"
					if product == "swap" {
						expectedPath = "/fapi/v1/klines"
					}
					requests := make(chan string, 4)
					server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests <- r.URL.Path + "?" + r.URL.Query().Get("interval") + "&" + r.URL.Query().Get("symbol")
						_, _ = fmt.Fprintf(w, `[[%d,"100","110","90","105","12",%d,"1234",10,"0","0","0"]]`, start.UnixMilli(), start.Add(barDuration-time.Millisecond).UnixMilli())
					}))
					t.Cleanup(server.Close)
					client := binanceapi.NewClient()
					client.HTTPClient = httpclient.NewHTTPClient(server.Client())
					require.NoError(t, client.SetSpotBaseURL(server.URL))
					require.NoError(t, client.SetSwapBaseURL(server.URL))
					adapter := binance.NewMarketDataAdapter(binance.AdapterConfig{ProductType: marketdata.ProductType(product), KlineCollector: binance.NewKlineCollector(client), Now: func() time.Time { return now }})
					registry := marketdata.NewRegistry()
					require.NoError(t, registry.Register(adapter))
					router, err := marketdata.NewRouter(registry, 2, nil, nil)
					require.NoError(t, err)
					storage := &capturedStorage{}
					h := NewHandler()
					h.Now = func() time.Time { return now }
					h.NewStorage = func(string, string, string) (marketfetch.Storage, error) { return storage, nil }
					// Replace only the network-bound registry. Production factory
					// selection, pipeline configuration, handler and writes remain real.
					h.NewMarketKlinePipeline = func(s marketfetch.Storage, market string, instrument marketdata.InstrumentType, provider, source string) (*marketfetch.KlinePipeline, error) {
						pipeline, err := NewMarketKlinePipeline(s, market, instrument, provider, source)
						if err == nil {
							pipeline.Router = router
						}
						return pipeline, err
					}
					t.Setenv("MOOX_SPACE_ID", "crypto")
					subject := "BTC-USDT-" + strings.ToUpper(product)
					var response *model.Response
					if mode == "timer" {
						t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "binance")
						t.Setenv("MOOX_MARKET_FETCH_SOURCE_ID", "")
						t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", product)
						t.Setenv("MOOX_MARKET_FETCH_MARKET_ID", "crypto")
						t.Setenv("MOOX_MARKET_FETCH_INSTRUMENT_TYPE", product)
						t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "bars")
						t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", frequency)
						t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", subject)
						t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", "{}")
						t.Setenv("MOOX_MARKET_FETCH_MODE", "")
						t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "storage")
						response, err = h.HandleTimerAt(context.Background(), "request", "function", now)
					} else {
						req := marketfetch.Request{BatchID: "batch", BatchKind: domain.BatchKindRealtime, SpaceID: "crypto", MarketID: "crypto", InstrumentType: product, DatasetID: "bars", Frequency: frequency, Provider: "binance", SourceID: product + "_http", MarketType: product, RequestID: "request", Items: []domain.CollectionItem{{SubjectID: subject, Symbol: "BTCUSDT", Provider: "binance", SourceID: product + "_http", MarketType: product, DataType: "kline", DatasetID: "bars", Frequency: frequency, BarLimit: 1}}}
						encoded, marshalErr := json.Marshal(req)
						require.NoError(t, marshalErr)
						var data map[string]interface{}
						require.NoError(t, json.Unmarshal(encoded, &data))
						response, err = h.HandleWithFunctionNameWithoutCompletion(context.Background(), model.CloudFunctionEvent{Action: model.EventActionMarketFetch, Data: data, StorageRPCGatewayTarget: "storage"}, "function")
					}
					require.NoError(t, err)
					require.True(t, response.Success, "%+v", response)
					require.Len(t, storage.rows, 1, "%+v", response)
					require.Equal(t, expectedPath+"?"+strings.ToLower(frequency)+"&BTCUSDT", <-requests)
					fields := map[string]*storagepb.TypedValue{}
					for _, field := range storage.rows[0].GetFields() {
						fields[field.GetFieldId()] = field.GetValue()
					}
					require.Equal(t, product+"_http", fields["source_id"].GetStringValue())
				})
			}
		}
	}
}

func TestCompositionRejectsUnknownAndCrossProductSources(t *testing.T) {
	for _, source := range []string{"unknown", "spot_http"} {
		_, err := NewMarketKlinePipeline(timerHandlerStorage{}, "crypto", marketdata.InstrumentSwap, "binance", source)
		require.Error(t, err)
	}
	_, err := NewMarketKlinePipeline(timerHandlerStorage{}, "crypto", marketdata.InstrumentSwap, "unknown", "swap_http")
	require.Error(t, err)
	_, err = ResolveSymbol("unknown", "crypto", "swap", "BTC-USDT-SWAP", "")
	require.Error(t, err)
}
