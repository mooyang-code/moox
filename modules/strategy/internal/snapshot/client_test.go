package snapshot

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func TestNormalizeStableHash(t *testing.T) {
	a, ha, _ := Normalize(Input{Data: []map[string]any{{"close": 1}}, Revision: "r", Cutoff: "t"})
	b, hb, _ := Normalize(Input{Data: []map[string]any{{"close": 1}}, Revision: "r", Cutoff: "t"})
	if len(a) != len(b) || ha != hb {
		t.Fatal()
	}
}

func TestValidateOutput_RejectsIncompletePayload(t *testing.T) {
	if err := ValidateOutput(domain.Output{}); err == nil {
		t.Fatal("expected validation error")
	}
	if err := ValidateOutput(domain.Output{Action: "hold", NextState: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
}

func TestCapture_RejectsMissingRevision(t *testing.T) {
	if _, err := Capture(Input{Data: []map[string]any{{"close": 1}}}); err == nil {
		t.Fatal("revision is required")
	}
}

func TestCaptureReturnsDetachedData(t *testing.T) {
	rows := []map[string]any{{"close": 1.0}}
	s, err := Capture(Input{Data: rows, Revision: "r", Cutoff: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	copyRows := s.Data()
	copyRows[0]["close"] = 99.0
	if got := s.Data()[0]["close"]; got != float64(1) {
		t.Fatalf("snapshot was mutable: %v", got)
	}
}
