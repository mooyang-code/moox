// Package requestauth implements MooX's canonical request-signing protocol.
package requestauth

import (
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	mooxcrypto "github.com/mooyang-code/moox/packages/crypto"
)

const (
	Version       = "moox-request-v1"
	HeaderAppID   = "X-App-Id"
	HeaderAppKey  = "X-App-Key"
	HeaderSpaceID = "X-Space-Id"
)

var signedHeaderNames = []string{HeaderAppID, HeaderAppKey, HeaderSpaceID}

type Material struct {
	Method    string
	Path      string
	Body      []byte
	Headers   map[string]string
	Timestamp int64
	Nonce     string
}

// Canonical validates and serializes request signing material.
func Canonical(m Material) ([]byte, error) {
	if strings.TrimSpace(m.Method) == "" {
		return nil, errors.New("method cannot be empty")
	}
	if m.Path == "" {
		return nil, errors.New("path cannot be empty")
	}
	parsed, err := url.Parse(m.Path)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || !strings.HasPrefix(m.Path, "/") {
		return nil, errors.New("path must not contain a scheme or host")
	}
	if m.Timestamp <= 0 {
		return nil, errors.New("timestamp must be positive")
	}
	if !isLowerHex(m.Nonce, 32) {
		return nil, errors.New("nonce must be 64 lowercase hex characters")
	}
	parts := []string{
		Version,
		strings.ToUpper(m.Method),
		m.Path,
	}
	if headers, ok, err := canonicalHeaders(m.Headers); err != nil {
		return nil, err
	} else if ok {
		parts = append(parts, headers...)
	}
	parts = append(parts,
		mooxcrypto.SHA256Hex(m.Body),
		strconv.FormatInt(m.Timestamp, 10),
		m.Nonce,
	)
	canonical := strings.Join(parts, "\n")
	return []byte(canonical), nil
}

func canonicalHeaders(headers map[string]string) ([]string, bool, error) {
	values := make(map[string]string, len(headers))
	for name, value := range headers {
		lowerName := strings.ToLower(strings.TrimSpace(name))
		for _, signedName := range signedHeaderNames {
			if lowerName == strings.ToLower(signedName) {
				value = strings.TrimSpace(value)
				if strings.ContainsAny(value, "\r\n") {
					return nil, false, errors.New("signed header values cannot contain newlines")
				}
				if value != "" {
					values[lowerName] = value
				}
			}
		}
	}
	if len(values) == 0 {
		return nil, false, nil
	}
	lines := make([]string, 0, len(signedHeaderNames))
	for _, name := range signedHeaderNames {
		lowerName := strings.ToLower(name)
		lines = append(lines, lowerName+":"+values[lowerName])
	}
	return lines, true, nil
}

func Sign(secret string, m Material) (string, error) {
	if secret == "" {
		return "", errors.New("secret cannot be empty")
	}
	canonical, err := Canonical(m)
	if err != nil {
		return "", err
	}
	return mooxcrypto.HMACSHA256Hex(secret, canonical), nil
}

func Verify(secret string, m Material, signature string) error {
	if !isLowerHex(signature, 32) {
		return errors.New("signature must be 64 lowercase hex characters")
	}
	expected, err := Sign(secret, m)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return errors.New("signature does not match")
	}
	return nil
}

func NewNonce() (string, error) {
	nonce, err := mooxcrypto.RandomHex(32)
	if err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return nonce, nil
}

func isLowerHex(value string, byteLen int) bool {
	if len(value) != byteLen*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == byteLen
}
