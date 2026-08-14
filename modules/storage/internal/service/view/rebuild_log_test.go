package view

import (
	"errors"
	"strings"
	"testing"
)

func TestRebuildErrorSummaryRedactsAndBoundsDetails(t *testing.T) {
	message := `password=secret token=abc {"api_key":"sekret","token":"json-token"} Authorization: Basic dXNlcjpwYXNz ` + strings.Repeat("x", 3000)
	got := rebuildErrorSummary(errors.New(message))
	for _, value := range []string{"secret", "abc", "sekret", "json-token", "dXNlcjpwYXNz"} {
		if strings.Contains(got, value) {
			t.Fatalf("sensitive values were not redacted: %q", got)
		}
	}
	if len(got) > 2048 {
		t.Fatalf("summary length = %d, want <= 2048", len(got))
	}
}
