package jetstream

import (
	"strings"
	"testing"
)

func TestSubjectTokenRoundTripUsesLowercaseUnpaddedBase32(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "foo", want: "mzxw6"},
		{value: "shard-01"},
		{value: "tenant/分片-1"},
		{value: "a.b:c"},
	} {
		t.Run(test.value, func(t *testing.T) {
			token, err := EncodeSubjectToken(test.value)
			if err != nil {
				t.Fatalf("EncodeSubjectToken() error = %v", err)
			}
			if test.want != "" && token != test.want {
				t.Fatalf("EncodeSubjectToken() = %q, want %q", token, test.want)
			}
			if token == "" || token != strings.ToLower(token) || strings.ContainsRune(token, '=') {
				t.Fatalf("EncodeSubjectToken() = %q, want lowercase unpadded token", token)
			}

			decoded, err := DecodeSubjectToken(token)
			if err != nil {
				t.Fatalf("DecodeSubjectToken() error = %v", err)
			}
			if decoded != test.value {
				t.Fatalf("DecodeSubjectToken() = %q, want %q", decoded, test.value)
			}
		})
	}
}

func TestSubjectTokenRejectsInvalidInput(t *testing.T) {
	for _, value := range []string{"", string([]byte{0xff, 0xfe})} {
		t.Run("encode", func(t *testing.T) {
			if _, err := EncodeSubjectToken(value); err == nil {
				t.Fatalf("EncodeSubjectToken(%q) succeeded, want error", value)
			}
		})
	}

	for _, token := range []string{"", "MY", "mzxw6=", "mzxw60", "mzxw6.", "7y", "a"} {
		t.Run(token, func(t *testing.T) {
			if _, err := DecodeSubjectToken(token); err == nil {
				t.Fatalf("DecodeSubjectToken(%q) succeeded, want error", token)
			}
		})
	}
}
