package requestauth

import (
	"regexp"
	"strings"
	"testing"
)

const testNonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testMaterial() Material {
	return Material{Method: "post", Path: "/api/admin/items/a%2Fb", Body: []byte("{\"a\":1}\n"), Timestamp: 1_700_000_000, Nonce: testNonce}
}

func TestCanonicalUsesExactBodyAndEscapedPath(t *testing.T) {
	got, err := Canonical(testMaterial())
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		Version,
		"POST",
		"/api/admin/items/a%2Fb",
		"e346432021b04179518d9614f3560ccd71354a4ee101ddcb893d6959a9d6301c",
		"1700000000",
		testNonce,
	}, "\n")
	if string(got) != want {
		t.Fatalf("Canonical() = %q, want %q", got, want)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	m := testMaterial()
	signature, err := Sign("secret", m)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify("secret", m, signature); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestVerifyRejectsBodyPathTimestampAndNonceChanges(t *testing.T) {
	original := testMaterial()
	signature, err := Sign("secret", original)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Material)
	}{
		{name: "body", mutate: func(m *Material) { m.Body = []byte("{\"a\":2}\n") }},
		{name: "path", mutate: func(m *Material) { m.Path = "/api/admin/items/a/b" }},
		{name: "timestamp", mutate: func(m *Material) { m.Timestamp++ }},
		{name: "nonce", mutate: func(m *Material) { m.Nonce = strings.Repeat("f", 64) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := original
			tt.mutate(&changed)
			if err := Verify("secret", changed, signature); err == nil {
				t.Fatal("Verify() accepted changed material")
			}
		})
	}
}

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

func TestVerifyRejectsMalformedSignature(t *testing.T) {
	for _, signature := range []string{"", "not-hex", strings.Repeat("a", 62), strings.Repeat("A", 64)} {
		if err := Verify("secret", testMaterial(), signature); err == nil {
			t.Fatalf("Verify() accepted %q", signature)
		}
	}
}
