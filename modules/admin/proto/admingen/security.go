package adminpb

import "strings"

// Masking implements the tRPC masking contract for ordinary secret responses.
func (x *Secret) Masking() {
	if x == nil {
		return
	}
	x.SecretValue = maskCredential(x.SecretValue)
}

func maskCredential(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + strings.Repeat("*", len(runes)-8) + string(runes[len(runes)-4:])
}
