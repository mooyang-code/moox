package hostmetricpb

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
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

// IsLegacyAgentID accepts the UUID identities written by older HostAgent
// releases during the rollout window. New identities must use IsAgentID.
func IsLegacyAgentID(id string) bool {
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		return false
	}
	for i, char := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

// IsCompatibleAgentID accepts current IDs and legacy UUIDs while queued old
// EventBus messages drain. The registry canonicalizes them to a compact ID.
func IsCompatibleAgentID(id string) bool { return IsAgentID(id) || IsLegacyAgentID(id) }

// CompactAgentIDForLegacy deterministically maps a UUID-era identity to the
// compact value used by both HostAgent and Monitor during migration. New
// hosts still use NewAgentID; this mapping only prevents the two processes
// from independently assigning different IDs to the same existing host.
func CompactAgentIDForLegacy(id string) (string, error) {
	if IsAgentID(id) {
		return id, nil
	}
	if !IsLegacyAgentID(id) {
		return "", errInvalidAgentID
	}
	hash := sha256.Sum256([]byte("moox-host-agent:" + strings.ToLower(id)))
	compact := make([]byte, AgentIDLength)
	for i := range compact {
		compact[i] = agentIDAlphabet[int(hash[i])%len(agentIDAlphabet)]
	}
	return string(compact), nil
}

var errInvalidAgentID = errors.New("invalid host agent id")

func ValidateAgentID(id string) error {
	if !IsCompatibleAgentID(id) {
		return errInvalidAgentID
	}
	return nil
}
