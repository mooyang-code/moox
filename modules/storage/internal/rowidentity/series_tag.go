package rowidentity

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const maxSeriesTagBytes = 128

// ValidateSeriesTag validates the opaque tag that distinguishes time-series
// rows sharing the rest of their identity. It never rewrites the tag.
func ValidateSeriesTag(tag string) error {
	if !utf8.ValidString(tag) {
		return errors.New("series_tag must be valid UTF-8")
	}
	if len(tag) > maxSeriesTagBytes {
		return errors.New("series_tag must not exceed 128 bytes")
	}
	if strings.TrimSpace(tag) != tag {
		return errors.New("series_tag must not have leading or trailing whitespace")
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] < 0x20 || tag[i] == 0x7f {
			return errors.New("series_tag must not contain ASCII control characters")
		}
	}
	return nil
}
