package combine

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"testing"
)

func TestCombine(t *testing.T) {
	got := Combine(map[string][]domain.TargetWeight{"a": {{InstrumentID: "BTC", TargetWeight: "0.5"}}, "b": {{InstrumentID: "BTC", TargetWeight: "-0.25"}}}, map[string]string{"a": "0.6", "b": "0.4"})
	if len(got) != 1 || got[0].TargetWeight != "0.200000000000" {
		t.Fatalf("%+v", got)
	}
}
