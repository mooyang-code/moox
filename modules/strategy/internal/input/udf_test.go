package input

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
)

func TestUDFRegistryValidatesParamsAndCanonicalizesIDs(t *testing.T) {
	registry := NewUDFRegistry()
	if err := registry.RegisterValidated("spot", func(params map[string]any) error {
		if _, ok := params["quote_asset"].(string); !ok {
			return context.Canceled
		}
		return nil
	}, func(context.Context, PoolUDFInput) ([]string, error) { return []string{"btc-usdt"}, nil }); err != nil {
		t.Fatal(err)
	}
	pool := config.Pool{UDF: &config.PoolUDF{Name: "spot", Params: map[string]any{"quote_asset": 123}}}
	if err := registry.Validate(pool); err == nil {
		t.Fatal("invalid UDF params should be rejected")
	}
	pool.UDF.Params = map[string]any{"quote_asset": "USDT"}
	ids, err := registry.Resolve(context.Background(), pool, []Subject{{InstrumentID: "BTC-USDT", Active: true}}, time.Now())
	if err != nil || len(ids) != 1 || ids[0] != "BTC-USDT" {
		t.Fatalf("resolved IDs=%v err=%v", ids, err)
	}
}
