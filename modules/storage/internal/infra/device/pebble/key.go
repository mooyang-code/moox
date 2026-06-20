package pebble

import (
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

const (
	timeSeriesKeyPrefix = "t"
	objectKeyPrefix     = "o"
	keyTimeLayout       = factkey.TimeVersionLayout
)

func encodeRowKey(row *pb.DataRow) string {
	if rowIsTimeSeries(row) {
		return encodeTimeSeriesRowKey(row)
	}
	return encodeLegacyObjectRowKey(row)
}

func encodeTimeSeriesRowKey(row *pb.DataRow) string {
	key := row.GetKey()
	scope := key.GetScope()
	dataTime := key.GetDataTime()
	if dataTime == "" {
		dataTime = factkey.EmptyVersion
	} else if normalized, ok := normalizeKeyTime(dataTime); ok {
		dataTime = normalized
	}
	rowID := key.GetRowId()
	if rowID == "" {
		rowID = factkey.EmptyVersion
	}
	return strings.Join([]string{
		timeSeriesKeyPrefix,
		escape(scope.GetSpaceId()),
		escape(scope.GetDatasetId()),
		escape(scope.GetSubjectId()),
		escape(scope.GetFreq()),
		escape(dataTime),
		escape(factkey.DimensionsHash(scope.GetDimensions())),
		escape(rowID),
	}, "|")
}

func encodeLegacyObjectRowKey(row *pb.DataRow) string {
	key := row.GetKey()
	scope := key.GetScope()
	objectID := key.GetRowId()
	if objectID == "" {
		objectID = scope.GetSubjectId()
	}
	version := factkey.EmptyVersion
	if key.GetDataTime() != "" {
		version = normalizeVersionForKey(key.GetDataTime())
	}
	return encodeObjectKeyParts(scope.GetSpaceId(), scope.GetDatasetId(), objectID, version)
}

func encodeObjectKeyParts(spaceID string, datasetID string, objectID string, version string) string {
	if objectID == "" {
		objectID = factkey.EmptyVersion
	}
	if version == "" {
		version = factkey.EmptyVersion
	}
	return strings.Join([]string{
		objectKeyPrefix,
		escape(spaceID),
		escape(datasetID),
		escape(objectID),
		escape(version),
	}, "|")
}

func rowIsTimeSeries(row *pb.DataRow) bool {
	key := row.GetKey()
	scope := key.GetScope()
	return scope.GetSubjectId() != "" && scope.GetFreq() != "" && key.GetDataTime() != ""
}

func encodeTimeSeriesScopePrefix(scope *pb.DataScope) string {
	parts := []string{timeSeriesKeyPrefix, escape(scope.GetSpaceId()), escape(scope.GetDatasetId())}
	if scope.GetSubjectId() != "" {
		parts = append(parts, escape(scope.GetSubjectId()))
	}
	if scope.GetSubjectId() != "" && scope.GetFreq() != "" {
		parts = append(parts, escape(scope.GetFreq()))
	}
	return strings.Join(parts, "|") + "|"
}

func readBounds(scope *pb.DataScope, timeRange *pb.TimeRange) ([]byte, []byte) {
	prefix := []byte(encodeTimeSeriesScopePrefix(scope))
	lower := prefix
	upper := nextPrefix(prefix)
	if scope.GetSubjectId() == "" || scope.GetFreq() == "" || timeRange == nil {
		return lower, upper
	}
	if start := timeRange.GetStartTime(); start != "" {
		if normalized, ok := normalizeKeyTime(start); ok {
			bound := []byte(string(prefix) + escape(normalized))
			lower = bound
		}
	}
	if end := timeRange.GetEndTime(); end != "" {
		if normalized, ok := normalizeKeyTime(end); ok {
			bound := []byte(string(prefix) + escape(normalized))
			upper = nextPrefix(bound)
		}
	}
	return lower, upper
}

func objectReadPrefix(scope *pb.DataScope, objectID string) string {
	parts := []string{objectKeyPrefix, escape(scope.GetSpaceId()), escape(scope.GetDatasetId())}
	if objectID != "" {
		parts = append(parts, escape(objectID))
	}
	return strings.Join(parts, "|") + "|"
}

func normalizeKeyTime(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value, false
	}
	return parsed.UTC().Format(keyTimeLayout), true
}

func normalizeVersionForKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return factkey.EmptyVersion
	}
	if normalized, ok := normalizeKeyTime(value); ok {
		return normalized
	}
	return value
}

func escape(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "|", "%7C")
	return value
}
