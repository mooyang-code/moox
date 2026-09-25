package crypto

import "testing"

func TestSubjectID(t *testing.T) {
	cases := []struct {
		base, quote, want string
		ok                bool
	}{
		{"BTC", "USDT", "BTC-USDT", true},
		{"1000pepe", "usdt", "1000PEPE-USDT", true},
		{"", "USDT", "", false},
		{"BTC", "", "", false},
		{"BT-C", "USDT", "", false},
	}
	for _, c := range cases {
		got, err := SubjectID(c.base, c.quote)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("SubjectID(%q,%q) = %q, %v", c.base, c.quote, got, err)
		}
	}
}
