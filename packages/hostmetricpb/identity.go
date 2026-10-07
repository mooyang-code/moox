package hostmetricpb

import (
	"crypto/rand"
	"errors"
	"fmt"
)

// AgentIDLength is the fixed display and routing identity size for a host.
// The alphabet deliberately excludes punctuation so the value is safe in
// EventBus subjects, alert keys, URLs, and Storage selectors.
const AgentIDLength = 4

const agentIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// NewAgentID allocates a cryptographically random compact host identity.
func NewAgentID() (string, error) {
	buf := make([]byte, AgentIDLength)
	for i := range buf {
		for {
			var sample [1]byte
			if _, err := rand.Read(sample[:]); err != nil {
				return "", fmt.Errorf("generate host agent id: %w", err)
			}
			// Reject the short tail of the byte range so every alphabet
			// character has the same probability.
			if sample[0] >= 248 {
				continue
			}
			buf[i] = agentIDAlphabet[int(sample[0])%len(agentIDAlphabet)]
			break
		}
	}
	return string(buf), nil
}

// IsAgentID reports whether id is a current four-character host identity.
func IsAgentID(id string) bool {
	if len(id) != AgentIDLength {
		return false
	}
	for _, char := range id {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')) {
			return false
		}
	}
	return true
}

var errInvalidAgentID = errors.New("invalid host agent id")

func ValidateAgentID(id string) error {
	if !IsAgentID(id) {
		return errInvalidAgentID
	}
	return nil
}
