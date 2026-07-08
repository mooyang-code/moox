package alerting

import "testing"

func TestAlertOwner(t *testing.T) {
	instances := []string{"monitor-c", "monitor-a", "monitor-b"}
	ownerA := Owner("check-a", "rule-a", instances)
	ownerB := Owner("check-a", "rule-a", []string{"monitor-b", "monitor-c", "monitor-a"})
	if ownerA == "" || ownerA != ownerB {
		t.Fatalf("owner not deterministic: %q %q", ownerA, ownerB)
	}
	if Owner("check-a", "rule-a", []string{"solo"}) != "solo" {
		t.Fatal("single instance should own all alerts")
	}
	if Owner("check-a", "rule-a", nil) != "" {
		t.Fatal("empty active instance list should return empty owner")
	}

	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		seen[Owner("check", string(rune('a'+i)), instances)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("ownership did not spread: %+v", seen)
	}
}
