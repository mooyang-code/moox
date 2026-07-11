package risk

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"testing"
)

func TestValidateRejectsGrossLimit(t *testing.T) {
	if err := Validate([]domain.TargetWeight{{InstrumentID: "A", TargetWeight: "1.1"}}, Policy{MaxGross: "1"}); err == nil {
		t.Fatal("expected risk rejection")
	}
}
