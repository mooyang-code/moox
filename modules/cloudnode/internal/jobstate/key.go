package jobstate

import (
	"encoding/base64"
	"strings"
)

func JobKey(spaceID, jobItemID string) string {
	return "job." + encodeSegment(spaceID) + "." + encodeSegment(jobItemID)
}

func encodeSegment(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(value)))
}
