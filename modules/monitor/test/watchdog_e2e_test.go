package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
)

func TestReadOnlyMarketCanaryUsesReadyAndBusinessQueryInsteadOfServiceRoot(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	bars := []watchdog.MarketBar{
		{DataTime: now.Add(-2 * time.Minute), Close: 100, Volume: 10, Closed: true},
		{DataTime: now.Add(-time.Minute), Close: 101, Volume: 12, Closed: true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			w.WriteHeader(http.StatusOK)
		case "/api/storage/bars":
			query := r.URL.Query()
			if query.Get("space_id") != "crypto" || query.Get("dataset_id") != "market_kline" ||
				query.Get("subject_id") != "BTC-USDT" || query.Get("freq") != "1m" || query.Get("limit") != "2" {
				http.Error(w, "unexpected canary scope", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(bars)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = root.Body.Close()
	if root.StatusCode != http.StatusNotFound {
		t.Fatalf("root status=%d, want 404 fixture", root.StatusCode)
	}
	ready, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = ready.Body.Close()
	if ready.StatusCode != http.StatusOK {
		t.Fatalf("ready status=%d", ready.StatusCode)
	}
	response, err := http.Get(server.URL + "/api/storage/bars?space_id=crypto&dataset_id=market_kline&subject_id=BTC-USDT&freq=1m&limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("business query status=%d", response.StatusCode)
	}
	var observed []watchdog.MarketBar
	if err := json.NewDecoder(response.Body).Decode(&observed); err != nil {
		t.Fatal(err)
	}
	result, err := watchdog.EvaluateMarketCanary(now, observed, watchdog.MarketCanaryConfig{
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fresh || result.Abnormal || !result.Watermark.Equal(now.Add(-time.Minute)) {
		t.Fatalf("canary=%+v", result)
	}
}
