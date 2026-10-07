package hostmetricpb

import (
	"regexp"
	"testing"
)

func TestNewAgentIDUsesCompactAlphaNumericShape(t *testing.T) {
	first, err := NewAgentID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAgentID()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{4}$`).MatchString(first) || !IsAgentID(first) {
		t.Fatalf("first id has invalid shape: %q", first)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{4}$`).MatchString(second) || !IsAgentID(second) {
		t.Fatalf("second id has invalid shape: %q", second)
	}
	if first == second {
		t.Fatalf("two allocations unexpectedly collided: %q", first)
	}
}
