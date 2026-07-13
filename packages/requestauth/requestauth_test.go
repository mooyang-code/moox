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

func TestVerifyRejectsSignedHeaderChanges(t *testing.T) {
	original := testMaterial()
	original.Headers = map[string]string{
		"X-App-Id":   "frontend",
		"X-App-Key":  "app-secret",
		"X-Space-Id": "space-1",
	}
	signature, err := Sign("secret", original)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range signedHeaderNames {
		changed := original
		changed.Headers = map[string]string{
			"X-App-Id":   original.Headers["X-App-Id"],
			"X-App-Key":  original.Headers["X-App-Key"],
			"X-Space-Id": original.Headers["X-Space-Id"],
		}
		changed.Headers[name] += "-tampered"
		if err := Verify("secret", changed, signature); err == nil {
			t.Fatalf("Verify() accepted changed %s", name)
		}
	}
}

func TestCanonicalSignedHeadersAreCaseInsensitiveAndOrdered(t *testing.T) {
	first := testMaterial()
	first.Headers = map[string]string{"x-space-id": " space-1 ", "X-App-Id": "frontend"}
	second := testMaterial()
	second.Headers = map[string]string{"x-app-id": "frontend", "X-SPACE-ID": "space-1"}

	gotFirst, err := Canonical(first)
	if err != nil {
		t.Fatal(err)
	}
	gotSecond, err := Canonical(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotFirst) != string(gotSecond) {
		t.Fatalf("canonical headers differ:\n%s\n---\n%s", gotFirst, gotSecond)
	}
	if !strings.Contains(string(gotFirst), "x-app-id:frontend\nx-app-key:\nx-space-id:space-1") {
		t.Fatalf("canonical headers missing or unordered: %q", gotFirst)
	}
}

func TestCanonicalIgnoresEmptySignedHeaders(t *testing.T) {
	withoutHeaders := testMaterial()
	withEmptyHeader := testMaterial()
	withEmptyHeader.Headers = map[string]string{"X-Space-Id": "  "}

	first, err := Canonical(withoutHeaders)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Canonical(withEmptyHeader)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("empty header changed canonical material: %q != %q", first, second)
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
