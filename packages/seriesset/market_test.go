package seriesset

import "testing"

func TestSeriesTagMarket(t *testing.T) {
	cases := map[string]string{
		"venue:binance|market:spot|source:spot_http": MarketSpot,
		"venue:binance|market:swap|source:swap_http": MarketSwap,
		"venue:binance":                              MarketSpot,
		"venue:okx|market:SWAP":                      MarketSwap,
		"default":                                    MarketSpot,
		"":                                           MarketSpot,
		"venue:binance|market:":                      MarketSpot,
	}
	for tag, want := range cases {
		if got := SeriesTagMarket(tag); got != want {
			t.Errorf("SeriesTagMarket(%q) = %q, want %q", tag, got, want)
		}
	}
}
