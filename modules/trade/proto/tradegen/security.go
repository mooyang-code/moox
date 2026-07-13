package tradepb

import "strings"

// Masking implements the tRPC masking contract for API credential responses.
func (x *ApiKey) Masking() {
	if x == nil {
		return
	}
	x.ApiKey = maskCredential(x.ApiKey)
	x.Passphrase = maskCredential(x.Passphrase)
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
