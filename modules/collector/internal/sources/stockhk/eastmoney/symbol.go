package eastmoney

import (
	"fmt"
	"strings"
)

func SecID(subjectID string) (string, error) {
	parts := strings.Split(strings.TrimSpace(subjectID), ".")
	if len(parts) != 2 || !strings.EqualFold(parts[1], "XHKG") {
		return "", fmt.Errorf("unsupported Hong Kong subject %q", subjectID)
	}
	code := strings.TrimSpace(parts[0])
	if len(code) == 0 || len(code) > 5 {
		return "", fmt.Errorf("Hong Kong code %q must contain 1-5 digits", code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("Hong Kong code %q must contain only digits", code)
		}
	}
	return "116." + strings.Repeat("0", 5-len(code)) + code, nil
}
