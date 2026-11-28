package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventAction_Constants_ShouldMatchExpectedValues(t *testing.T) {
	assert.Equal(t, EventAction("market_fetch"), EventActionMarketFetch)
	assert.Equal(t, EventAction("egress_probe"), EventActionEgressProbe)
}
