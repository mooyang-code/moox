package view

import (
	"errors"
	"strings"
	"testing"
)

func TestRebuildErrorSummaryRedactsAndBoundsDetails(t *testing.T) {
	message := "password=secret token=abc " + strings.Repeat("x", 3000)
	got := rebuildErrorSummary(errors.New(message))
	if strings.Contains(got, "secret") || strings.Contains(got, "abc") {
		t.Fatalf("sensitive values were not redacted: %q", got)
	}
	if len(got) > 2048 {
		t.Fatalf("summary length = %d, want <= 2048", len(got))
	}
}
