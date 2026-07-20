package pebble

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// The first byte is the physical namespace. Field and attribute values are
// deliberately separate so a user supplied name can never collide with the
// other value kind. There is intentionally no row-marker namespace.
const (
	fieldNamespace     byte = 0x01
	attributeNamespace byte = 0x02
	timeSeriesKind     byte = 0x01
	recordKind         byte = 0x02
)

func appendPart(dst []byte, value string) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(len(value)))
	dst = append(dst, buf[:n]...)
	return append(dst, value...)
}

func appendRawPart(dst []byte, value []byte) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(len(value)))
	dst = append(dst, buf[:n]...)
	return append(dst, value...)
}

func rowParts(key *pb.RowKey, bucketDuration time.Duration) (kind byte, parts [][]byte, err error) {
	if key == nil || key.GetSpaceId() == "" || key.GetDatasetId() == "" {
		return 0, nil, fmt.Errorf("row key space_id and dataset_id are required")
	}
	base := [][]byte{[]byte(key.GetSpaceId()), []byte(key.GetDatasetId())}
	switch value := key.GetKind().(type) {
	case *pb.RowKey_TimeSeries:
		row := value.TimeSeries
		if row == nil || row.GetSubjectId() == "" || row.GetFreq() == "" || row.GetDataTime() == "" {
			return 0, nil, fmt.Errorf("time-series row key requires subject_id, freq and data_time")
		}
		at, parseErr := time.Parse(time.RFC3339Nano, row.GetDataTime())
		if parseErr != nil {
			return 0, nil, fmt.Errorf("invalid data_time: %w", parseErr)
		}
		if bucketDuration <= 0 {
			bucketDuration = 24 * time.Hour
		}
		nanos := at.UTC().UnixNano()
		bucket := time.Unix(0, nanos-(nanos%bucketDuration.Nanoseconds())).UTC().Format(time.RFC3339Nano)
		return timeSeriesKind, append(base, []byte(row.GetSubjectId()), []byte(row.GetFreq()), []byte(bucket), []byte(row.GetDataTime())), nil
	case *pb.RowKey_Record:
		row := value.Record
		if row == nil || row.GetRecordId() == "" {
			return 0, nil, fmt.Errorf("record row key requires record_id")
		}
		return recordKind, append(base, []byte(row.GetRecordId()), []byte(row.GetVersion())), nil
	default:
		return 0, nil, fmt.Errorf("row key kind is required")
	}
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
		return nil, fmt.Errorf("field_id is required")
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
		return nil, fmt.Errorf("attribute key is required")
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

func decodePart(data []byte) (string, []byte, error) {
	n, consumed := binary.Uvarint(data)
	if consumed <= 0 || uint64(len(data[consumed:])) < n {
		return "", nil, fmt.Errorf("invalid key component")
	}
	end := consumed + int(n)
	return string(data[consumed:end]), data[end:], nil
}

func isNamespaceKey(key []byte, namespace byte) bool {
	return len(key) > 0 && key[0] == namespace
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

func sameRowPrefix(a, b []byte) bool { return bytes.Equal(a, b) }
