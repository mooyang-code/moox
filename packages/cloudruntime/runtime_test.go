package cloudruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/packages/jetstream"
)

func testConfig(target string) Config {
	return Config{
		ServiceGatewayTarget: target, SpaceID: "crypto", NodeID: "node-1", CodePackageID: "pkg",
		Auth: AuthConfig{AccessKey: "key", SecretKey: "secret", TargetNode: "gateway-1"},
	}
}

func resetRegistryForTest() {
	globalRegistry.mu.Lock()
	defer globalRegistry.mu.Unlock()
	globalRegistry.handlers = map[string]Handler{}
}

func TestExecuteJobItemReportsBeforeAck(t *testing.T) {
	resetRegistryForTest()
	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		return Result{Summary: map[string]any{"rows": 1}}, nil
	}))
	reported := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reported = true
		var body reportRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Status != reportSuccess || body.JobItemID != "item-1" {
			t.Fatalf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()
	result := ExecuteJobItem(context.Background(), testConfig(server.URL), JobItem{
		SpaceID: "crypto", JobItemID: "item-1", JobType: "collect.kline", CodePackageID: "pkg",
	}, 1, 3)
	if !reported || result.Decision != jetstream.ACK {
		t.Fatalf("reported=%v result=%+v", reported, result)
	}
}

func TestExecuteJobItemRetryableFailureOnlyReportsOnLastDelivery(t *testing.T) {
	resetRegistryForTest()
	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		return Result{}, Retryable(errors.New("temporary"), "TEMP")
	}))
	reports := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports++
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()
	item := JobItem{SpaceID: "crypto", JobItemID: "item-1", JobType: "collect.kline", CodePackageID: "pkg"}
	first := ExecuteJobItem(context.Background(), testConfig(server.URL), item, 1, 3)
	if first.Decision != jetstream.RETRY || reports != 0 {
		t.Fatalf("first=%+v reports=%d", first, reports)
	}
	last := ExecuteJobItem(context.Background(), testConfig(server.URL), item, 3, 3)
	if last.Decision != jetstream.TERM || reports != 1 {
		t.Fatalf("last=%+v reports=%d", last, reports)
	}
}

func TestExecuteJobItemReportFailureRetriesDelivery(t *testing.T) {
	resetRegistryForTest()
	Register("collect.kline", HandlerFunc(func(context.Context, JobItem) (Result, error) {
		return Result{}, nil
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	result := ExecuteJobItem(context.Background(), testConfig(server.URL), JobItem{
		SpaceID: "crypto", JobItemID: "item-1", JobType: "collect.kline", CodePackageID: "pkg",
	}, 1, 3)
	if result.Decision != jetstream.RETRY || result.Err == nil {
		t.Fatalf("result=%+v", result)
	}
}
