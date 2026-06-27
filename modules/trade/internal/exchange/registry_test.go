package exchange

import "testing"

func TestRegistryNew(t *testing.T) {
	if _, err := New("not_exist"); err == nil {
		t.Fatal("New(not_exist) want error, got nil")
	}

	Register("stub", func() ExchangeAdapter { return nil })

	names := Names()
	found := false
	for _, n := range names {
		if n == "stub" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Names() = %v, want contains stub", names)
	}
}
