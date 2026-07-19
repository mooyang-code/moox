package jetstream

import (
	"strings"
	"testing"
)

func TestShardTokenRoundTripUsesLowercaseUnpaddedBase32(t *testing.T) {
	for _, test := range []struct {
		shardID string
		want    string
	}{
		{shardID: "foo", want: "mzxw6"},
		{shardID: "shard-01"},
		{shardID: "tenant/分片-1"},
		{shardID: "a.b:c"},
	} {
		t.Run(test.shardID, func(t *testing.T) {
			token, err := EncodeShardToken(test.shardID)
			if err != nil {
				t.Fatalf("EncodeShardToken() error = %v", err)
			}
			if test.want != "" && token != test.want {
				t.Fatalf("EncodeShardToken() = %q, want %q", token, test.want)
			}
			if token == "" || token != strings.ToLower(token) || strings.ContainsRune(token, '=') {
				t.Fatalf("EncodeShardToken() = %q, want lowercase unpadded token", token)
			}

			decoded, err := DecodeShardToken(token)
			if err != nil {
				t.Fatalf("DecodeShardToken() error = %v", err)
			}
			if decoded != test.shardID {
				t.Fatalf("DecodeShardToken() = %q, want %q", decoded, test.shardID)
			}
		})
	}
}

func TestShardTokenRejectsInvalidInput(t *testing.T) {
	for _, shardID := range []string{"", string([]byte{0xff, 0xfe})} {
		t.Run("encode", func(t *testing.T) {
			if _, err := EncodeShardToken(shardID); err == nil {
				t.Fatalf("EncodeShardToken(%q) succeeded, want error", shardID)
			}
		})
	}

	for _, token := range []string{"", "MY", "mzxw6=", "mzxw60", "mzxw6.", "7y", "a"} {
		t.Run(token, func(t *testing.T) {
			if _, err := DecodeShardToken(token); err == nil {
				t.Fatalf("DecodeShardToken(%q) succeeded, want error", token)
			}
		})
	}
}
