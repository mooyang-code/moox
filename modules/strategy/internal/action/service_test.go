package action

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"testing"
)

func TestValidateDelegatedToEngine(t *testing.T) {
	if err := engine.Validate(domain.Output{Action: domain.ActionHold}); err != nil {
		t.Fatal(err)
	}
}
