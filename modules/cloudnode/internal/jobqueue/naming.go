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
	DefaultSubjectPrefix    = "moox.cloudnode"
	DefaultExecStream       = "MOOX_CLOUDNODE_EXEC"
	DefaultProjectionStream = "MOOX_CLOUDNODE_PROJECTION"

	ProjectionEventJobItemSubmitted = "jobitem.submitted"
	ProjectionEventJobItemRunning   = "jobitem.running"
	ProjectionEventJobItemReported  = "jobitem.reported"
	ProjectionEventJobItemCanceled  = "jobitem.canceled"
	ProjectionEventNodeHeartbeat    = "node.heartbeat"
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
	if token == "" || len(token) > 64 {
		sum := sha256.Sum256([]byte(raw))
		return "x" + hex.EncodeToString(sum[:])[:16]
	}
	return token
}

// ExecSubject returns the exact subject used for one executable JobItem message.
func ExecSubject(cfg NamingConfig, spaceID, codePackageID, jobType string) string {
	prefix := subjectPrefix(cfg)
	return prefix + ".exec.v1.jobitem.s." + SubjectToken(spaceID) +
		".pkg." + SubjectToken(codePackageID) +
		".type." + SubjectToken(jobType)
}

// ExecFilterSubject returns the consumer filter subject for a node and job type.
func ExecFilterSubject(cfg NamingConfig, spaceID, codePackageID, jobType string) string {
	return ExecSubject(cfg, spaceID, codePackageID, jobType)
}

// ExecStreamSubject returns the wildcard subject configured on the execution stream.
func ExecStreamSubject(cfg NamingConfig) string {
	return subjectPrefix(cfg) + ".exec.v1.>"
}

// ProjectionSubject returns the subject for a CloudNode projection event.
func ProjectionSubject(cfg NamingConfig, event string) string {
	return subjectPrefix(cfg) + ".projection.v1." + strings.Trim(event, ".")
}

// ProjectionStreamSubject returns the wildcard subject configured on the projection stream.
func ProjectionStreamSubject(cfg NamingConfig) string {
	return subjectPrefix(cfg) + ".projection.v1.>"
}

// ConsumerName returns a durable consumer name for a specific executable route.
func ConsumerName(spaceID, codePackageID, jobType string) string {
	return "cn_exec_" + SubjectToken(spaceID) + "_" + SubjectToken(codePackageID) + "_" + SubjectToken(jobType)
}

func subjectPrefix(cfg NamingConfig) string {
	prefix := strings.Trim(cfg.SubjectPrefix, ". ")
	if prefix == "" {
		return DefaultSubjectPrefix
	}
	return prefix
}
