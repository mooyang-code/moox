package binance

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestEndpointAttemptContextKeepsAMinimumWindow(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	attempt, release := endpointAttemptContext(parent, 7)
	defer release()
	deadline, ok := attempt.Deadline()
	if !ok {
		t.Fatal("endpoint attempt must have a deadline")
	}
	if window := time.Until(deadline); window < 1900*time.Millisecond {
		t.Fatalf("endpoint window %v is shorter than the 2s minimum", window)
	}

	short, shortCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer shortCancel()
	limited, limitedRelease := endpointAttemptContext(short, 7)
	defer limitedRelease()
	limitedDeadline, _ := limited.Deadline()
	parentDeadline, _ := short.Deadline()
	if limitedDeadline.After(parentDeadline) {
		t.Fatal("an endpoint window must never outlive its parent")
	}
}

func TestTryEndpointsFailsOverInOrder(t *testing.T) {
	var called []string
	klines, err := tryEndpoints(context.Background(), []string{"a", "b", "c"}, func(_ context.Context, domain string) ([]*exchange.Kline, error) {
		called = append(called, domain)
		if domain == "c" {
			return []*exchange.Kline{{}}, nil
		}
		return nil, errors.New(domain + " down")
	})
	if err != nil || len(klines) != 1 {
		t.Fatalf("klines=%d err=%v", len(klines), err)
	}
	if strings.Join(called, ",") != "a,b,c" {
		t.Fatalf("called %v, want a,b,c", called)
	}
	if _, err := tryEndpoints(context.Background(), []string{"a"}, func(context.Context, string) ([]*exchange.Kline, error) {
		return nil, errors.New("only down")
	}); err == nil || err.Error() != "only down" {
		t.Fatalf("err=%v", err)
	}
}
