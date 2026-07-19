package doctor

import (
	"strings"
	"testing"
	"time"
)

func TestContractEnums(t *testing.T) {
	t.Parallel()

	if got := []Mode{ModeBootstrap, ModeDiagnose}; len(got) != 2 {
		t.Fatalf("modes = %v", got)
	}
	statuses := []CheckStatus{StatusPass, StatusWarn, StatusFail, StatusUnknown, StatusBlocked, StatusSkipped}
	for _, status := range statuses {
		if err := status.Validate(); err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
	}
	conclusions := []Conclusion{ConclusionHealthy, ConclusionDegraded, ConclusionUnhealthy, ConclusionInconclusive}
	for _, conclusion := range conclusions {
		if err := conclusion.Validate(); err != nil {
			t.Fatalf("conclusion %q: %v", conclusion, err)
		}
	}
}

func TestObservationLimits(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	valid := Observation{
		Source:     "monitor.reporter",
		ObservedAt: now,
		ExpiresAt:  now.Add(time.Minute),
		Summary:    "reporter is fresh",
		Digest:     "sha256:" + strings.Repeat("a", 64),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}

	valid.Summary = string(make([]byte, MaxObservationSummaryBytes+1))
	if err := valid.Validate(); err == nil {
		t.Fatal("oversized observation summary accepted")
	}

	valid.Summary = "valid"
	valid.Digest = "sha256:short"
	if err := valid.Validate(); err == nil {
		t.Fatal("invalid observation digest accepted")
	}
}
