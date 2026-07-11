package engine

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"testing"
)

func TestValidateOutput(t *testing.T) {
	if err := Validate(domain.Output{Action: domain.ActionRebalance, Targets: []domain.TargetWeight{{InstrumentID: "BTC", TargetWeight: "1"}}, NextState: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
}
