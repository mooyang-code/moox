package backtest

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"testing"
)

func TestHashDecisionStable(t *testing.T) {
	o := domain.Output{Action: domain.ActionHold, NextState: map[string]any{}}
	if HashDecision(o) != HashDecision(o) {
		t.Fatal()
	}
}
