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

func TestAgentIDCompatibility(t *testing.T) {
	if !IsLegacyAgentID("550e8400-e29b-41d4-a716-446655440000") {
		t.Fatal("legacy UUID was rejected")
	}
	if !IsCompatibleAgentID("aB3x") || !IsAgentID("aB3x") {
		t.Fatal("compact ID was rejected")
	}
	for _, value := range []string{"", "abc", "a-b1", "host-1", "550e8400-e29b-41d4-a716-44665544000z"} {
		if IsCompatibleAgentID(value) {
			t.Fatalf("invalid ID accepted: %q", value)
		}
	}
}

func TestCompactAgentIDForLegacyIsDeterministic(t *testing.T) {
	legacy := "550e8400-e29b-41d4-a716-446655440000"
	first, err := CompactAgentIDForLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CompactAgentIDForLegacy(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !IsAgentID(first) {
		t.Fatalf("legacy mapping is not stable compact id: %q vs %q", first, second)
	}
}
