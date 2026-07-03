package cloudruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestPollWorkItemsSendsSpaceID(t *testing.T) {
	var got pollWorkItemsRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/service/cloudnode/PollWorkItems" {
			t.Fatalf("path = %s, want /api/service/cloudnode/PollWorkItems", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(pollWorkItemsResponse{
			RetInfo: &retInfo{Code: 0, Msg: "ok"},
		})
	}))
	defer server.Close()

	host, port := testServerAddress(t, server.URL)
	_, err := pollWorkItems(context.Background(), Config{
		ServerIP:           host,
		ServerPort:         port,
		SpaceID:            "crypto",
		NodeID:             "node-a",
		SupportedWorkloads: []string{"collector.binance.spot.kline"},
		Auth: AuthConfig{
			AccessKey: "test-ak",
			SecretKey: "test-sk",
		},
	})
	if err != nil {
		t.Fatalf("pollWorkItems returned error: %v", err)
	}
	if got.SpaceID != "crypto" {
		t.Fatalf("space_id = %q, want crypto", got.SpaceID)
	}
}

func testServerAddress(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return parsed.Hostname(), port
}
