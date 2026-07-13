package healthz

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestRequestMetricsFiltersPublishToProtectedRegistry(t *testing.T) {
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithCalleeServiceName("trpc.moox.test.Service")
	msg.WithCalleeMethod("Call")

	if _, err := requestMetricsServerFilter(ctx, struct{}{}, func(context.Context, interface{}) (interface{}, error) {
		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("client failed")
	if err := requestMetricsClientFilter(ctx, struct{}{}, struct{}{}, func(context.Context, interface{}, interface{}) error {
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("client filter error = %v", err)
	}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"moox_trpc_server_requests_total": false,
		"moox_trpc_client_requests_total": false,
	}
	for _, family := range families {
		if _, ok := wanted[family.GetName()]; ok {
			wanted[family.GetName()] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("metric %s was not gathered", name)
		}
	}
}
