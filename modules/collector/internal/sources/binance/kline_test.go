package binance

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/stretchr/testify/require"
)

func TestBuildKlineRowsUsesBinanceSeriesTag(t *testing.T) {
	now := time.Now().UTC()
	kline := market.NewKline("binance", "BTC-USDT", "1h")
	kline.OpenTime = now.Add(-2 * time.Hour)
	kline.CloseTime = now.Add(-time.Hour)
	kline.Open = common.NewDecimal("1")
	kline.High = common.NewDecimal("2")
	kline.Low = common.NewDecimal("1")
	kline.Close = common.NewDecimal("2")
	kline.Volume = common.NewDecimal("3")
	kline.QuoteVolume = common.NewDecimal("4")

	rows, err := buildKlineRows([]*market.Kline{kline}, "crypto", "spot_kline_1h", "BTC-USDT", "1H")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, binanceSeriesTag, rows[0].GetKey().GetTimeSeries().GetSeriesTag())
}
