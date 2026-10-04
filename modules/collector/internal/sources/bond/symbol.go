package bond

import (
	"fmt"
	"strings"
)

func EastMoneySecID(subjectID string) (string, error) {
	parts := strings.Split(strings.TrimSpace(subjectID), ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("convertible bond subject %q must use CODE.EXCHANGE", subjectID)
	}
	code := strings.TrimSpace(parts[0])
	exchange := strings.ToUpper(strings.TrimSpace(parts[1]))
	if len(code) != 6 || strings.ContainsAny(code, " ,/\\") {
		return "", fmt.Errorf("convertible bond code %q must contain six digits", code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("convertible bond code %q must contain only digits", code)
		}
	}
	switch exchange {
	case "XSHG":
		return "1." + code, nil
	case "XSHE":
		return "0." + code, nil
	default:
		return "", fmt.Errorf("unsupported convertible bond exchange %q", exchange)
	}
}

func SinaSymbol(subjectID string) (string, error) {
	parts := strings.Split(strings.TrimSpace(subjectID), ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("convertible bond subject %q must use CODE.EXCHANGE", subjectID)
	}
	code := strings.TrimSpace(parts[0])
	exchange := strings.ToUpper(strings.TrimSpace(parts[1]))
	if len(code) != 6 {
		return "", fmt.Errorf("convertible bond code %q must contain six digits", code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("convertible bond code %q must contain only digits", code)
		}
	}
	switch exchange {
	case "XSHG":
		return "sh" + code, nil
	case "XSHE":
		return "sz" + code, nil
	default:
		return "", fmt.Errorf("unsupported convertible bond exchange %q", exchange)
	}
}
