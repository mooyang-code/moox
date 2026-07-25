package cloudjobqueue

import "testing"

func TestIdentityGolden(t *testing.T) {
	identity := Identity{SpaceID: "crypto", CodePackageID: "pkg-a", JobType: "collect.kline"}
	name, err := identity.ConsumerName()
	if err != nil {
		t.Fatal(err)
	}
	if name != "cn_exec_b527eba5dfae2513ff578b90" {
		t.Fatalf("ConsumerName() = %q", name)
	}
	subject, err := identity.SubjectID()
	if err != nil {
		t.Fatal(err)
	}
	if subject != "pkg-a/collect.kline" {
		t.Fatalf("SubjectID() = %q", subject)
	}
}

func TestIdentityRejectsEmptyOrSurroundingWhitespace(t *testing.T) {
	for _, test := range [][3]string{
		{"", "pkg", "collect.kline"},
		{"crypto", " pkg", "collect.kline"},
		{"crypto", "pkg", "collect.kline "},
	} {
		if _, err := (Identity{SpaceID: test[0], CodePackageID: test[1], JobType: test[2]}).ConsumerName(); err == nil {
			t.Fatalf("ConsumerName(%q, %q, %q) succeeded", test[0], test[1], test[2])
		}
	}
}
