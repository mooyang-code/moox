package index

import (
	"fmt"
	"strings"
)

func EastMoneySecID(subjectID string) (string, error) {
	code, exchange, err := splitSubject(subjectID)
	if err != nil {
		return "", err
	}
	switch exchange {
	case "XSHG":
		return "1." + code, nil
	case "XSHE", "XBSE":
		return "0." + code, nil
	default:
		return "", fmt.Errorf("unsupported index exchange %q", exchange)
	}
}

func RawCode(subjectID string) (string, error) {
	code, _, err := splitSubject(subjectID)
	return code, err
}

func splitSubject(subjectID string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(subjectID), ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("index subject %q must use CODE.EXCHANGE", subjectID)
	}
	code := strings.TrimSpace(parts[0])
	exchange := strings.ToUpper(strings.TrimSpace(parts[1]))
	if code == "" || len(code) > 12 || strings.ContainsAny(code, " ,/\\") {
		return "", "", fmt.Errorf("invalid index code %q", code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", "", fmt.Errorf("invalid index code %q", code)
		}
	}
	switch exchange {
	case "XSHG", "XSHE", "XBSE":
	default:
		return "", "", fmt.Errorf("unsupported index exchange %q", exchange)
	}
	return code, exchange, nil
}
