package binance

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/stretchr/testify/require"
)

func TestKlineCollectorFallsBackToNextSpotEndpoint(t *testing.T) {
	failed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failed.Close()
	working := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != binanceapi.SpotKlineEndpoint {
			t.Fatalf("path = %q, want %q", r.URL.Path, binanceapi.SpotKlineEndpoint)
		}
		_, _ = w.Write([]byte(`[[1700000000000,"1","2","0.5","1.5","3",1700000059999,"4",1,"2","3","0"]]`))
	}))
	defer working.Close()

	client := binanceapi.NewClient()
	require.NoError(t, client.SetSpotBaseURLs([]string{failed.URL, working.URL}))
	client.HTTPClient = httpclient.NewHTTPClient(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}})
	collector := &KlineCollector{client: client, spotAPI: binanceapi.NewSpotAPI(client)}

	rows, err := collector.fetchKlinesOnce(context.Background(), &sources.CollectParams{InstType: InstTypeSPOT, Symbol: "BTCUSDT"}, &exchange.KlineRequest{Symbol: "BTCUSDT", Interval: "1m", Limit: 1})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assertEqualTime(t, time.UnixMilli(1700000000000), rows[0].OpenTime)
}

func assertEqualTime(t *testing.T, want, got time.Time) {
	t.Helper()
	if !want.Equal(got) {
		t.Fatalf("time = %s, want %s", got, want)
	}
}
