package factkey

import (
	"errors"
	"strings"
	"time"
)

const (
	EmptyVersion      = "_"
	TimeVersionLayout = "2006-01-02T15:04:05.000000000Z"
)

func BuildTimeSeriesDataKey(subjectID string, freq string, dimensions map[string]string) string {
	return strings.Join([]string{
		EscapePart(subjectID),
		EscapePart(freq),
		EscapePart(DimensionsHash(dimensions)),
	}, "|")
}

func BuildObjectDataKey(objectID string) (string, error) {
	if strings.TrimSpace(objectID) == "" {
		return "", errors.New("object_id is required")
	}
	return EscapePart(objectID), nil
}

func NormalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return EmptyVersion
	}
	if normalized, err := NormalizeTimeVersion(value); err == nil {
		return normalized
	}
	return EscapePart(value)
}

func NormalizeTimeVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("time version is required")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", err
	}
	return parsed.UTC().Format(TimeVersionLayout), nil
}

func EscapePart(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "|", "%7C")
	return value
}
