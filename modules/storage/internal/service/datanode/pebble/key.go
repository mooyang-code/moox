package pebble

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/rowidentity"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// The first byte is the physical namespace. Field and attribute values are
// deliberately separate so a user supplied name can never collide with the
// other value kind. There is intentionally no row-marker namespace.
const (
	fieldNamespace      byte = 0x01
	attributeNamespace  byte = 0x02
	historyNamespace    byte = 0x03
	timeSeriesKind      byte = 0x01
	recordKind          byte = 0x02
	canonicalTimeLayout      = "2006-01-02T15:04:05.000000000Z"
)

func appendPart(dst []byte, value string) []byte {
	return appendRawPart(dst, []byte(value))
}

func appendRawPart(dst []byte, value []byte) []byte {
	for _, b := range value {
		if b == 0 {
			dst = append(dst, 0, 0xff)
			continue
		}
		dst = append(dst, b)
	}
	return append(dst, 0, 0)
}

func rowParts(key *pb.RowKey, bucketDuration time.Duration) (kind byte, parts [][]byte, err error) {
	if key == nil || key.GetSpaceId() == "" || key.GetDatasetId() == "" {
		return 0, nil, invalid("row key space_id and dataset_id are required")
	}
	base := [][]byte{[]byte(key.GetSpaceId()), []byte(key.GetDatasetId())}
	switch value := key.GetKind().(type) {
	case *pb.RowKey_TimeSeries:
		row := value.TimeSeries
		if row == nil || row.GetSubjectId() == "" || row.GetFreq() == "" || row.GetDataTime() == "" {
			return 0, nil, invalid("time-series row key requires subject_id, freq and data_time")
		}
		at, parseErr := time.Parse(time.RFC3339Nano, row.GetDataTime())
		if parseErr != nil {
			return 0, nil, invalidf("invalid data_time: %w", parseErr)
		}
		if bucketDuration <= 0 {
			bucketDuration = 24 * time.Hour
		}
		at = at.UTC()
		bucket := at.Truncate(bucketDuration).Format(canonicalTimeLayout)
		dataTime := at.Format(canonicalTimeLayout)
		if tagErr := rowidentity.ValidateSeriesTag(row.GetSeriesTag()); tagErr != nil {
			return 0, nil, invalidf("invalid series_tag: %w", tagErr)
		}
		return timeSeriesKind, append(base, []byte(bucket), []byte(row.GetSubjectId()), []byte(row.GetFreq()), []byte(dataTime), []byte(row.GetSeriesTag())), nil
	case *pb.RowKey_Record:
		row := value.Record
		if row == nil || row.GetRecordId() == "" || row.GetVersion() == "" {
			return 0, nil, invalid("record row key requires record_id and version")
		}
		return recordKind, append(base, []byte(row.GetRecordId()), []byte(row.GetVersion())), nil
	default:
		return 0, nil, invalid("row key kind is required")
	}
}

func NormalizeRowKey(key *pb.RowKey) (*pb.RowKey, error) {
	if key == nil {
		return nil, invalid("row key is required")
	}
	normalized := proto.Clone(key).(*pb.RowKey)
	if row := normalized.GetTimeSeries(); row != nil {
		at, err := time.Parse(time.RFC3339Nano, row.GetDataTime())
		if err != nil {
			return nil, invalidf("invalid data_time: %w", err)
		}
		row.DataTime = at.UTC().Format(canonicalTimeLayout)
	}
	if _, _, err := rowParts(normalized, 24*time.Hour); err != nil {
		return nil, err
	}
	return normalized, nil
}

func encodeRowPrefix(key *pb.RowKey, bucketDuration time.Duration) ([]byte, error) {
	kind, parts, err := rowParts(key, bucketDuration)
	if err != nil {
		return nil, err
	}
	out := []byte{kind}
	for _, part := range parts {
		out = appendRawPart(out, part)
	}
	return out, nil
}

func encodeFieldKey(key *pb.RowKey, fieldID string, bucketDuration time.Duration) ([]byte, error) {
	if fieldID == "" {
		return nil, invalid("field_id is required")
	}
	prefix, err := encodeRowPrefix(key, bucketDuration)
	if err != nil {
		return nil, err
	}
	out := append([]byte{fieldNamespace}, prefix...)
	return appendPart(out, fieldID), nil
}

func encodeAttributeKey(key *pb.RowKey, attribute string, bucketDuration time.Duration) ([]byte, error) {
	if attribute == "" {
		return nil, invalid("attribute key is required")
	}
	prefix, err := encodeRowPrefix(key, bucketDuration)
	if err != nil {
		return nil, err
	}
	out := append([]byte{attributeNamespace}, prefix...)
	return appendPart(out, attribute), nil
}

func encodeNamespacePrefix(namespace byte, key *pb.RowKey, bucketDuration time.Duration) ([]byte, error) {
	prefix, err := encodeRowPrefix(key, bucketDuration)
	if err != nil {
		return nil, err
	}
	return append([]byte{namespace}, prefix...), nil
}

// encodeHistoryKey is a dedicated logical row index. Unlike field keys, it
// orders data_time before subject/frequency so a backfill cursor can seek
// directly to the next logical row instead of rescanning a subject-first day
// bucket on every page.
func encodeHistoryKey(key *pb.RowKey, bucketDuration time.Duration) ([]byte, error) {
	kind, parts, err := rowParts(key, bucketDuration)
	if err != nil {
		return nil, err
	}
	if kind != timeSeriesKind {
		return nil, invalid("history index requires a time-series row key")
	}
	out := []byte{historyNamespace, timeSeriesKind}
	for _, index := range []int{0, 1, 5, 3, 4, 6} {
		out = appendRawPart(out, parts[index])
	}
	return out, nil
}

// parseTimeSeriesFieldKey extracts the row identity from a legacy field or
// attribute key. Older stores predate the dedicated history namespace, but
// their row identity is still encoded losslessly in every field key. The
// history reader uses this parser once per dataset to backfill the compact
// time-ordered index without reading field values.
func parseTimeSeriesFieldKey(key []byte) (*pb.RowKey, bool) {
	if len(key) < 2 || (key[0] != fieldNamespace && key[0] != attributeNamespace) || key[1] != timeSeriesKind {
		return nil, false
	}
	rest := key[2:]
	parts := make([]string, 0, 7)
	for i := 0; i < 7; i++ {
		part, next, err := decodePart(rest)
		if err != nil {
			return nil, false
		}
		parts = append(parts, part)
		rest = next
	}
	if len(parts) != 7 {
		return nil, false
	}
	return &pb.RowKey{
		SpaceId:   parts[0],
		DatasetId: parts[1],
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: parts[3],
			Freq:      parts[4],
			DataTime:  parts[5],
			SeriesTag: parts[6],
		}},
	}, true
}

func decodePart(data []byte) (string, []byte, error) {
	value := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if data[i] != 0 {
			value = append(value, data[i])
			i++
			continue
		}
		if i+1 >= len(data) {
			return "", nil, fmt.Errorf("invalid key component")
		}
		switch data[i+1] {
		case 0:
			return string(value), data[i+2:], nil
		case 0xff:
			value = append(value, 0)
			i += 2
		default:
			return "", nil, fmt.Errorf("invalid key component")
		}
	}
	return "", nil, fmt.Errorf("invalid key component")
}

func nextPrefix(prefix []byte) []byte {
	result := append([]byte(nil), prefix...)
	for i := len(result) - 1; i >= 0; i-- {
		if result[i] != 0xff {
			result[i]++
			return result[:i+1]
		}
	}
	return nil
}
