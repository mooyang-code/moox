package jetstream

import (
	"encoding/base32"
	"fmt"
	"unicode/utf8"
)

var shardTokenEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// EncodeShardToken encodes a UTF-8 shard ID as one lowercase NATS subject token.
func EncodeShardToken(shardID string) (string, error) {
	if shardID == "" {
		return "", fmt.Errorf("shard_id is required")
	}
	if !utf8.ValidString(shardID) {
		return "", fmt.Errorf("shard_id must be valid UTF-8")
	}
	return shardTokenEncoding.EncodeToString([]byte(shardID)), nil
}

// DecodeShardToken decodes and validates a lowercase, unpadded shard token.
func DecodeShardToken(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("shard token is required")
	}
	for i := 0; i < len(token); i++ {
		if !isShardTokenByte(token[i]) {
			return "", fmt.Errorf("invalid shard token %q", token)
		}
	}
	raw, err := shardTokenEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("invalid shard token %q: %w", token, err)
	}
	if !utf8.Valid(raw) || len(raw) == 0 {
		return "", fmt.Errorf("shard token %q does not decode to a non-empty UTF-8 shard_id", token)
	}
	shardID := string(raw)
	canonical, err := EncodeShardToken(shardID)
	if err != nil || canonical != token {
		return "", fmt.Errorf("invalid non-canonical shard token %q", token)
	}
	return shardID, nil
}

func isShardTokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '2' && b <= '7'
}
