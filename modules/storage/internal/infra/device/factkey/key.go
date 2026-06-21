package factkey

import (
	"errors"
	"fmt"
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

func BuildRecordDataKey(recordID string) (string, error) {
	if strings.TrimSpace(recordID) == "" {
		return "", errors.New("record_id is required")
	}
	return EscapePart(recordID), nil
}

func ParseTimeSeriesDataKey(value string) (subjectID string, freq string, dimHash string, err error) {
	parts := strings.Split(value, "|")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("invalid time series data_key")
	}
	return UnescapePart(parts[0]), UnescapePart(parts[1]), UnescapePart(parts[2]), nil
}

func ParseRecordDataKey(value string) string {
	return UnescapePart(value)
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

func UnescapePart(value string) string {
	value = strings.ReplaceAll(value, "%7C", "|")
	value = strings.ReplaceAll(value, "%25", "%")
	return value
}
