package jetstream

import (
	"fmt"
	"strings"
)

func validateSubject(subject string) error {
	if strings.TrimSpace(subject) == "" {
		return fmt.Errorf("subject is required")
	}
	if strings.ContainsAny(subject, " \t\r\n\x00") {
		return fmt.Errorf("subject contains whitespace or NUL")
	}
	if strings.Contains(subject, ">") {
		return fmt.Errorf("publish subject cannot contain >")
	}
	for _, token := range strings.Split(subject, ".") {
		if token == "" || token == "*" {
			return fmt.Errorf("publish subject contains wildcard or empty token")
		}
	}
	return nil
}
