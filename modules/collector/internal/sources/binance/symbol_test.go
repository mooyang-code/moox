package binance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/stretchr/testify/require"
)

func TestFetchSymbolsFallsBackToNextSpotEndpoint(t *testing.T) {
	var hosts []string
	client := binanceapi.NewClient()
	require.NoError(t, client.SetSpotBaseURLs([]string{"https://first.invalid", "https://second.invalid"}))
	client.HTTPClient = httpclient.NewHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			hosts = append(hosts, req.URL.Host)
			if req.URL.Host == "first.invalid" {
				return nil, errors.New("first endpoint unavailable")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"timezone":"UTC","serverTime":1,"symbols":[{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","filters":[]}]}`)),
			}, nil
		}),
	})
	collector := &SymbolCollector{client: client, spotAPI: binanceapi.NewSpotAPI(client)}

	symbols, err := collector.fetchSymbols(context.Background(), &sources.CollectParams{InstType: InstTypeSPOT})
	require.NoError(t, err)
	require.Len(t, symbols, 1)
	require.Equal(t, "BTC-USDT", symbols[0].Symbol)
	require.Equal(t, []string{"first.invalid", "second.invalid"}, hosts)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
