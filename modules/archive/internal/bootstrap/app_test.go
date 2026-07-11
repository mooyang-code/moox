package bootstrap

import (
	"testing"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
)

func TestSourceLists(t *testing.T) {
	cfg := testConfig()
	got := sourceLists(cfg)
	if len(got["crypto_binance"]) != 2 || got["crypto_binance"][0] != "spot_kline" {
		t.Fatalf("source lists=%v", got)
	}
}

func testConfig() *config.Config { return config.Default() }
