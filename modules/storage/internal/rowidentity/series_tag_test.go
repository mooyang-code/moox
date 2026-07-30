package rowidentity

import (
	"strings"
	"testing"
)

func TestValidateSeriesTagAcceptsValidValuesUnchanged(t *testing.T) {
	for _, tag := range []string{"", "venue:binance", "市场:现货", "venue/binance@spot+v2"} {
		if err := ValidateSeriesTag(tag); err != nil {
			t.Errorf("ValidateSeriesTag(%q) = %v", tag, err)
		}
	}
}

func TestValidateSeriesTagRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		tag  string
	}{
		{name: "invalid UTF-8", tag: string([]byte{0xff})},
		{name: "more than 128 bytes", tag: strings.Repeat("a", 129)},
		{name: "NUL", tag: "venue:\x00binance"},
		{name: "ASCII control", tag: "venue:\tbinance"},
		{name: "DEL", tag: "venue:\x7fbinance"},
		{name: "leading whitespace", tag: " venue:binance"},
		{name: "trailing whitespace", tag: "venue:binance "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateSeriesTag(tt.tag); err == nil {
				t.Fatalf("ValidateSeriesTag(%q) accepted invalid tag", tt.tag)
			}
		})
	}
}

func TestValidateSeriesTagUsesByteLimit(t *testing.T) {
	if err := ValidateSeriesTag(strings.Repeat("界", 42)); err != nil {
		t.Fatalf("126-byte Unicode tag rejected: %v", err)
	}
	if err := ValidateSeriesTag(strings.Repeat("界", 43)); err == nil {
		t.Fatal("129-byte Unicode tag accepted")
	}
}
