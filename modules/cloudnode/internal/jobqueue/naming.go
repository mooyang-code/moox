// Package jobqueue owns CloudNode execution queue naming and JetStream adapters.
package jobqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
)

const (
	DefaultSubjectPrefix = "moox.cloudnode"
	DefaultExecStream    = "MOOX_CLOUDNODE_EXEC"
)

// NamingConfig controls stream and subject names.
type NamingConfig struct {
	SubjectPrefix string
}

// ValidateNamingConfig rejects prefixes that collide with other MooX event domains.
func ValidateNamingConfig(cfg NamingConfig) error {
	prefix := subjectPrefix(cfg)
	if prefix == "moox.storage" || strings.HasPrefix(prefix, "moox.storage.") {
		return fmt.Errorf("cloudnode subject_prefix %q conflicts with storage subjects", prefix)
	}
	return nil
}

// SubjectToken converts arbitrary IDs into a safe single NATS subject token.
func SubjectToken(raw string) string {
	identity := raw
	raw = strings.TrimSpace(strings.ToLower(raw))
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r) || r == '.' || r == '/' || r == ':':
			b.WriteByte('_')
		default:
			b.WriteByte('_')
		}
	}
	token := strings.Trim(b.String(), "_")
	for strings.Contains(token, "__") {
		token = strings.ReplaceAll(token, "__", "_")
	}
	if token == "" {
		token = "x"
	}
	if len(token) > 40 {
		token = strings.TrimRight(token[:40], "_")
	}
	sum := sha256.Sum256([]byte(identity))
	return token + "_" + hex.EncodeToString(sum[:])[:16]
}

// ExecFilterSubject returns the consumer filter subject for a node and job type.
func ExecFilterSubject(cfg NamingConfig, spaceID, codePackageID, jobType string) string {
	return "moox.cloudnode.job.requested.v1.>"
}

// ExecStreamSubject returns the wildcard subject configured on the execution stream.
func ExecStreamSubject(cfg NamingConfig) string {
	return "moox.cloudnode.>"
}

// ConsumerName returns a durable consumer name for a specific executable route.
func ConsumerName(spaceID, codePackageID, jobType string) string {
	identity := fmt.Sprintf("%d:%s%d:%s%d:%s", len(spaceID), spaceID, len(codePackageID), codePackageID, len(jobType), jobType)
	sum := sha256.Sum256([]byte(identity))
	return "cn_exec_" + hex.EncodeToString(sum[:])[:24]
}

func subjectPrefix(cfg NamingConfig) string {
	prefix := strings.Trim(cfg.SubjectPrefix, ". ")
	if prefix == "" {
		return DefaultSubjectPrefix
	}
	return prefix
}
