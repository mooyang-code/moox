package cloudjobqueue

import "testing"

func TestIdentityGolden(t *testing.T) {
	identity := Identity{SpaceID: "crypto", JobType: "collect.kline"}
	name, err := identity.ConsumerName()
	if err != nil {
		t.Fatal(err)
	}
	if name != "cn_exec_d4571cb5bd260f184b902fda" {
		t.Fatalf("ConsumerName() = %q", name)
	}
	subject, err := identity.SubjectID()
	if err != nil {
		t.Fatal(err)
	}
	if subject != "collect.kline" {
		t.Fatalf("SubjectID() = %q", subject)
	}
}

func TestIdentityRejectsEmptyOrSurroundingWhitespace(t *testing.T) {
	for _, test := range [][2]string{
		{"", "collect.kline"},
		{" crypto", "collect.kline"},
		{"crypto", "collect.kline "},
	} {
		if _, err := (Identity{SpaceID: test[0], JobType: test[1]}).ConsumerName(); err == nil {
			t.Fatalf("ConsumerName(%q, %q) succeeded", test[0], test[1])
		}
	}
}
