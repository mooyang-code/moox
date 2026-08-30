package input

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/stretchr/testify/require"
)

func TestReadinessCheckerRequiresEveryPoolFactor(t *testing.T) {
	pool := PoolResult{Items: []PoolItem{{InstrumentID: "BTC"}, {InstrumentID: "ETH"}}}
	values := map[string]InstrumentInput{
		"BTC": {Values: map[string]quant.Decimal{"bias": quant.One()}},
		"ETH": {Values: map[string]quant.Decimal{}},
	}
	err := (ReadinessChecker{}).Check(pool, values, []string{"bias"})
	require.ErrorContains(t, err, "ETH:bias")
}

func TestReadinessCheckerIgnoresHistoryIneligibility(t *testing.T) {
	err := (ReadinessChecker{}).Check(PoolResult{Ineligible: map[string]string{"ETH": "insufficient_history"}}, nil, nil)
	require.NoError(t, err)
}

func TestReadinessCheckerRequiresCurrentSourceRow(t *testing.T) {
	pool := PoolResult{Items: []PoolItem{{InstrumentID: "BTC"}}}
	values := map[string]InstrumentInput{"BTC": {PoolItem: PoolItem{InstrumentID: "BTC"}, Values: map[string]quant.Decimal{"bias": quant.Must("1")}}}
	err := (ReadinessChecker{}).CheckWithPresence(pool, values, map[string]bool{}, []string{"bias"})
	if !errors.Is(err, ErrStrictIncomplete) {
		t.Fatalf("expected ErrStrictIncomplete, got %v", err)
	}
	var incomplete *StrictIncompleteError
	if !errors.As(err, &incomplete) || len(incomplete.Pool.Items) != 1 || incomplete.Pool.Items[0].InstrumentID != "BTC" {
		t.Fatalf("strict incomplete error lost resolved pool: %v", err)
	}
}
