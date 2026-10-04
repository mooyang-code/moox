package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBinanceSeriesTagMatchesCollectorIdentity(t *testing.T) {
	tests := []struct {
		marketType string
		frequency  string
		want       string
		wantErr    bool
	}{
		{marketType: "spot", frequency: "1m", want: "venue:binance|market:spot|source:spot_http"},
		{marketType: "swap", frequency: "1m", want: "venue:binance|market:swap|source:swap_http"},
		{marketType: "spot", frequency: "1H", want: "venue:binance"},
		{marketType: "swap", frequency: "1H", want: "venue:binance"},
		{marketType: "future", frequency: "1m", wantErr: true},
		{marketType: "spot", frequency: "15m", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.marketType+"/"+test.frequency, func(t *testing.T) {
			got, err := binanceSeriesTag(test.marketType, test.frequency)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}
