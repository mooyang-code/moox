package jetstream

import (
	"encoding/base32"
	"fmt"
	"unicode/utf8"
)

var subjectTokenEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// EncodeSubjectToken encodes a UTF-8 identifier as one lowercase NATS subject token.
func EncodeSubjectToken(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("subject token value is required")
	}
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("subject token value must be valid UTF-8")
	}
	return subjectTokenEncoding.EncodeToString([]byte(value)), nil
}

// DecodeSubjectToken decodes and validates a lowercase, unpadded subject token.
func DecodeSubjectToken(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("subject token is required")
	}
	for i := 0; i < len(token); i++ {
		if !isSubjectTokenByte(token[i]) {
			return "", fmt.Errorf("invalid subject token %q", token)
		}
	}
	raw, err := subjectTokenEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("invalid subject token %q: %w", token, err)
	}
	if !utf8.Valid(raw) || len(raw) == 0 {
		return "", fmt.Errorf("subject token %q does not decode to non-empty UTF-8", token)
	}
	value := string(raw)
	canonical, err := EncodeSubjectToken(value)
	if err != nil || canonical != token {
		return "", fmt.Errorf("invalid non-canonical subject token %q", token)
	}
	return value, nil
}

func isSubjectTokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '2' && b <= '7'
}
