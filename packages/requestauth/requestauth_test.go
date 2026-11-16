package requestauth

import (
	"regexp"
	"testing"
)

func TestNewNonceReturns64LowercaseHexCharacters(t *testing.T) {
	first, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("NewNonce() = %q", first)
	}
	if first == second {
		t.Fatal("NewNonce() returned a duplicate")
	}
}
