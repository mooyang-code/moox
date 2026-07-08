package jobstate

import (
	"encoding/base64"
	"strings"
)

func JobKey(spaceID, jobItemID string) string {
	return "job." + encodeSegment(spaceID) + "." + encodeSegment(jobItemID)
}

func SpacePrefix(spaceID string) string {
	return "job." + encodeSegment(spaceID) + "."
}

func ParseJobKey(key string) (spaceID string, jobItemID string, ok bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0] != "job" {
		return "", "", false
	}
	spaceID, ok = decodeSegment(parts[1])
	if !ok {
		return "", "", false
	}
	jobItemID, ok = decodeSegment(parts[2])
	if !ok {
		return "", "", false
	}
	return spaceID, jobItemID, true
}

func encodeSegment(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(value)))
}

func decodeSegment(value string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", false
	}
	return string(raw), true
}
