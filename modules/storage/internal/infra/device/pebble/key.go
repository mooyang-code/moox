package pebble

import (
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

const (
	timeSeriesKeyPrefix = "t"
	recordKeyPrefix     = "r"
)

func encodeRowKey(row *pb.PrimaryStoreRow) string {
	return encodePrimaryStoreKey(row.GetKey())
}

func encodePrimaryStoreKey(key *pb.PrimaryStoreKey) string {
	return strings.Join([]string{
		kindPrefix(key.GetDataKind()),
		escape(key.GetSpaceId()),
		escape(key.GetDatasetId()),
		escape(key.GetKey()),
		escape(normalizeVersionForKey(key.GetVersion())),
	}, "|")
}

func encodeKeyPrefix(key *pb.PrimaryStoreKey) string {
	return strings.Join([]string{
		kindPrefix(key.GetDataKind()),
		escape(key.GetSpaceId()),
		escape(key.GetDatasetId()),
		escape(key.GetKey()),
	}, "|") + "|"
}

func encodeDatasetPrefix(kind pb.DataKind, spaceID string, datasetID string) string {
	return strings.Join([]string{
		kindPrefix(kind),
		escape(spaceID),
		escape(datasetID),
	}, "|") + "|"
}

func keyBounds(key *pb.PrimaryStoreKey, versionRange *pb.VersionRange) ([]byte, []byte) {
	prefix := []byte(encodeKeyPrefix(key))
	lower := prefix
	upper := nextPrefix(prefix)
	if versionRange == nil {
		return lower, upper
	}
	if start := versionRange.GetStartVersion(); start != "" {
		lower = []byte(string(prefix) + escape(normalizeVersionForKey(start)))
	}
	if end := versionRange.GetEndVersion(); end != "" {
		upper = nextPrefix([]byte(string(prefix) + escape(normalizeVersionForKey(end))))
	}
	return lower, upper
}

func kindPrefix(kind pb.DataKind) string {
	switch kind {
	case pb.DataKind_DATA_KIND_TIME_SERIES:
		return timeSeriesKeyPrefix
	case pb.DataKind_DATA_KIND_RECORD:
		return recordKeyPrefix
	default:
		return ""
	}
}

func normalizeVersionForKey(value string) string {
	return factkey.NormalizeVersion(value)
}

func escape(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "|", "%7C")
	return value
}
