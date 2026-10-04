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
