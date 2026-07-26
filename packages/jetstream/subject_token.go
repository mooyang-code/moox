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
