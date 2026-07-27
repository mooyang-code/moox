package binance

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKlineCursorInitialRequestUsesSmallRecentWindow(t *testing.T) {
	cursor := newKlineCursor(nil)

	req, ok := cursor.NextRequest("BTCUSDT", "1m")
	require.True(t, ok)
	assert.Equal(t, 10, req.Limit)
	assert.True(t, req.StartTime.IsZero())

	more, err := cursor.Advance([]*exchange.Kline{{OpenTime: time.Now().UTC()}})
	require.NoError(t, err)
	assert.False(t, more)
}

func TestKlineCursorCatchupStartsAfterWatermarkAndCapsAtFiveThousand(t *testing.T) {
	watermark := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	cursor := newKlineCursor(&watermark)
	last := watermark

	for page := 0; page < 5; page++ {
		req, ok := cursor.NextRequest("BTCUSDT", "1m")
		require.True(t, ok)
		assert.Equal(t, 1000, req.Limit)
		assert.True(t, req.StartTime.After(last))

		rows := make([]*exchange.Kline, 1000)
		for i := range rows {
			rows[i] = &exchange.Kline{OpenTime: req.StartTime.Add(time.Duration(i) * time.Minute)}
		}
		last = rows[len(rows)-1].OpenTime
		more, err := cursor.Advance(rows)
		require.NoError(t, err)
		assert.Equal(t, page < 4, more)
	}

	_, ok := cursor.NextRequest("BTCUSDT", "1m")
	assert.False(t, ok)
}

func TestKlineCursorRejectsPageWithoutProgress(t *testing.T) {
	watermark := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	cursor := newKlineCursor(&watermark)
	_, ok := cursor.NextRequest("BTCUSDT", "1m")
	require.True(t, ok)

	_, err := cursor.Advance([]*exchange.Kline{{OpenTime: watermark}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not advance")
}
