package events

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/jetstream"
)

type SubjectTemplate struct {
	raw string
}

func NewSubjectTemplate(raw string) (SubjectTemplate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SubjectTemplate{}, fmt.Errorf("subject template is required")
	}
	check := strings.ReplaceAll(strings.ReplaceAll(raw, "<space>", "x"), "<subject>", "x")
	if strings.ContainsAny(check, " \t\r\n*><") {
		return SubjectTemplate{}, fmt.Errorf("subject template contains invalid characters")
	}
	if strings.Count(raw, "<space>") != 1 || strings.Count(raw, "<subject>") != 1 {
		return SubjectTemplate{}, fmt.Errorf("subject template must contain one <space> and one <subject>")
	}
	return SubjectTemplate{raw: raw}, nil
}

func (t SubjectTemplate) Render(spaceID, subjectID string) (string, error) {
	if t.raw == "" {
		return "", fmt.Errorf("subject template is empty")
	}
	spaceToken, err := jetstream.EncodeSubjectToken(spaceID)
	if err != nil {
		return "", fmt.Errorf("encode space_id: %w", err)
	}
	subjectToken, err := jetstream.EncodeSubjectToken(subjectID)
	if err != nil {
		return "", fmt.Errorf("encode subject_id: %w", err)
	}
	return strings.ReplaceAll(strings.ReplaceAll(t.raw, "<space>", spaceToken), "<subject>", subjectToken), nil
}

func (t SubjectTemplate) Pattern() string { return t.raw }

// FamilyPattern returns the wildcard topic family covered by this template.
// The literal prefix is the governed routing identity; everything after the
// first routing placeholder belongs to the event instance and is covered by
// the family wildcard. NATS `>` must be the final token, so a template suffix
// is intentionally included in this prefix family rather than re-emitted
// after the wildcard.
func (t SubjectTemplate) FamilyPattern() string {
	if t.raw == "" {
		return ""
	}
	prefix := t.raw
	for _, placeholder := range []string{"<space>", "<subject>"} {
		if index := strings.Index(prefix, placeholder); index >= 0 {
			prefix = prefix[:index]
		}
	}
	prefix = strings.TrimSuffix(prefix, ".")
	if prefix == "" {
		return ">"
	}
	return prefix + ".>"
}
