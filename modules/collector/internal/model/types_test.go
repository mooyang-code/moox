package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventAction_Constants_ShouldMatchExpectedValues(t *testing.T) {
	assert.Equal(t, EventAction("market_fetch"), EventActionMarketFetch)
	assert.Equal(t, EventAction("egress_probe"), EventActionEgressProbe)
}

func TestCollectParams_ShouldMarshal(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	params := CollectParams{Symbol: "BTCUSDT", Interval: "1m", StartTime: &start, Limit: 100}
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "BTCUSDT")

}
