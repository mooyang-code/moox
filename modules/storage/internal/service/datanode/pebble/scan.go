package pebble

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// ScanFields enumerates row identities from the field/attribute namespaces and
// then reads the requested values through the same point-read path as normal
// PrimaryStore reads. It never reads a View index, so View initial builds cannot
// accidentally recurse through the materialized view they are creating.
func (s *Store) ScanFields(ctx context.Context, spaceID, datasetID string, kind pb.DataKind, timeRange *pb.TimeRange, versionRange *pb.VersionRange, fieldIDs, attributeKeys []string, page *pb.Page, pageToken string, order pb.SortOrder) ([]*pb.RowFieldValues, *pb.PageResult, string, error) {
	if s == nil || s.db == nil {
		return nil, nil, "", errors.New("pebble store is closed")
	}
	if spaceID == "" || datasetID == "" {
		return nil, nil, "", invalid("space_id and dataset_id are required")
	}
	if kind != pb.DataKind_DATA_KIND_TIME_SERIES && kind != pb.DataKind_DATA_KIND_RECORD {
		return nil, nil, "", invalid("data_kind must be time_series or record")
	}
	start, end, err := scanBounds(timeRange, versionRange, kind)
	if err != nil {
		return nil, nil, "", err
	}
	if (len(fieldIDs) != 0) != (len(attributeKeys) != 0) && order != pb.SortOrder_SORT_ORDER_DESC {
		return s.scanNamespacePage(ctx, spaceID, datasetID, kind, start, end, fieldIDs, attributeKeys, page, pageToken)
	}

	requestedFields := stringSet(fieldIDs)
	requestedAttributes := stringSet(attributeKeys)
	keys := make(map[string]*pb.RowKey)
	fieldNames := make(map[string]map[string]struct{})
	attributeNames := make(map[string]map[string]struct{})
	namespaceList := []struct {
		value byte
		attrs bool
	}{
		{fieldNamespace, false},
		{attributeNamespace, true},
	}
	kindByte := timeSeriesKind
	if kind == pb.DataKind_DATA_KIND_RECORD {
		kindByte = recordKind
	}
	for _, namespace := range namespaceList {
		prefix := []byte{namespace.value, kindByte}
		prefix = appendRawPart(prefix, []byte(spaceID))
		prefix = appendRawPart(prefix, []byte(datasetID))
		upper := nextPrefix(prefix)
		iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: upper})
		if err != nil {
			return nil, nil, "", err
		}
		for valid := iter.First(); valid; valid = iter.Next() {
			if err := ctx.Err(); err != nil {
				_ = iter.Close()
				return nil, nil, "", err
			}
			parts, ok := decodePhysicalParts(iter.Key()[2:])
			if !ok {
				continue
			}
			rowKey, valueName, ok := decodeScannedRow(parts, kind, spaceID, datasetID)
			if !ok || !scanKeyInRange(rowKey, start, end) {
				continue
			}
			if namespace.attrs {
				if len(requestedAttributes) != 0 {
					if _, ok := requestedAttributes[valueName]; !ok {
						continue
					}
				}
			} else if len(requestedFields) != 0 {
				if _, ok := requestedFields[valueName]; !ok {
					continue
				}
			}
			id := rowKeyIdentity(rowKey)
			keys[id] = rowKey
			if namespace.attrs {
				if attributeNames[id] == nil {
					attributeNames[id] = make(map[string]struct{})
				}
				attributeNames[id][valueName] = struct{}{}
			} else {
				if fieldNames[id] == nil {
					fieldNames[id] = make(map[string]struct{})
				}
				fieldNames[id][valueName] = struct{}{}
			}
		}
		iterErr := iter.Error()
		_ = iter.Close()
		if iterErr != nil {
			return nil, nil, "", iterErr
		}
	}
	if len(fieldIDs) == 0 && len(attributeKeys) == 0 {
		fieldIDs = unionNames(fieldNames)
		attributeKeys = unionNames(attributeNames)
	}

	ordered := make([]*pb.RowKey, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool { return scanKeyLess(ordered[i], ordered[j], kind, order) })
	total := len(ordered)
	pageNo, pageSize := scanPage(page)
	offset := (int(pageNo) - 1) * pageSize
	if pageToken != "" {
		cursorData, err := base64.RawURLEncoding.DecodeString(pageToken)
		if err != nil {
			return nil, nil, "", invalid("invalid page_token")
		}
		cursor := &pb.RowKey{}
		if err := proto.Unmarshal(cursorData, cursor); err != nil || cursor.GetSpaceId() != spaceID || cursor.GetDatasetId() != datasetID {
			return nil, nil, "", invalid("invalid page_token")
		}
		offset = 0
		for offset < len(ordered) {
			if order == pb.SortOrder_SORT_ORDER_DESC {
				if scanKeyLess(ordered[offset], cursor, kind, pb.SortOrder_SORT_ORDER_ASC) {
					break
				}
			} else if scanKeyLess(cursor, ordered[offset], kind, pb.SortOrder_SORT_ORDER_ASC) {
				break
			}
			offset++
		}
	}
	if offset >= len(ordered) {
		return nil, &pb.PageResult{Page: uint32(pageNo), Size: uint32(pageSize), Total: uint32(total), HasMore: false}, "", nil
	}
	endOffset := offset + pageSize
	if endOffset > len(ordered) {
		endOffset = len(ordered)
	}
	ordered = ordered[offset:endOffset]
	rows, err := s.ReadFields(ctx, ordered, fieldIDs, attributeKeys)
	if err != nil {
		return nil, nil, "", err
	}
	next := ""
	if endOffset < total {
		cursorData, err := proto.Marshal(ordered[len(ordered)-1])
		if err != nil {
			return nil, nil, "", err
		}
		next = base64.RawURLEncoding.EncodeToString(cursorData)
	}
	return rows, &pb.PageResult{Page: uint32(pageNo), Size: uint32(pageSize), Total: uint32(total), HasMore: endOffset < total}, next, nil
}

func (s *Store) scanNamespacePage(ctx context.Context, spaceID, datasetID string, kind pb.DataKind, start, end string, fieldIDs, attributeKeys []string, page *pb.Page, pageToken string) ([]*pb.RowFieldValues, *pb.PageResult, string, error) {
	namespace := fieldNamespace
	if len(fieldIDs) == 0 {
		namespace = attributeNamespace
	}
	kindByte := timeSeriesKind
	if kind == pb.DataKind_DATA_KIND_RECORD {
		kindByte = recordKind
	}
	prefix := []byte{namespace, kindByte}
	prefix = appendRawPart(prefix, []byte(spaceID))
	prefix = appendRawPart(prefix, []byte(datasetID))
	lower := prefix
	if pageToken != "" {
		cursor, err := base64.RawURLEncoding.DecodeString(pageToken)
		if err != nil || !bytes.HasPrefix(cursor, prefix) {
			return nil, nil, "", invalid("invalid page_token")
		}
		lower = nextPrefix(cursor)
	}
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: nextPrefix(prefix)})
	if err != nil {
		return nil, nil, "", err
	}
	defer iter.Close()
	_, pageSize := scanPage(page)
	keys := make([]*pb.RowKey, 0, pageSize)
	var lastRowPrefix, lastReturned []byte
	hasMore := false
	for valid := iter.First(); valid; valid = iter.Next() {
		if err := ctx.Err(); err != nil {
			return nil, nil, "", err
		}
		parts, ok := decodePhysicalParts(iter.Key()[2:])
		if !ok {
			continue
		}
		key, _, ok := decodeScannedRow(parts, kind, spaceID, datasetID)
		if !ok || !scanKeyInRange(key, start, end) {
			continue
		}
		rowPrefix, err := encodeNamespacePrefix(namespace, key, s.bucketDuration)
		if err != nil || bytes.Equal(lastRowPrefix, rowPrefix) {
			continue
		}
		if len(keys) == pageSize {
			hasMore = true
			break
		}
		lastRowPrefix = rowPrefix
		lastReturned = append(lastReturned[:0], rowPrefix...)
		keys = append(keys, key)
	}
	rows, err := s.ReadFields(ctx, keys, fieldIDs, attributeKeys)
	if err != nil {
		return nil, nil, "", err
	}
	next := ""
	if hasMore {
		next = base64.RawURLEncoding.EncodeToString(lastReturned)
	}
	pageNo, _ := scanPage(page)
	return rows, &pb.PageResult{Page: uint32(pageNo), Size: uint32(pageSize), HasMore: hasMore}, next, nil
}

func decodeScannedRow(parts []string, kind pb.DataKind, spaceID, datasetID string) (*pb.RowKey, string, bool) {
	if len(parts) < 1 || parts[0] != spaceID || len(parts) < 2 || parts[1] != datasetID {
		return nil, "", false
	}
	switch kind {
	case pb.DataKind_DATA_KIND_TIME_SERIES:
		if len(parts) != 7 {
			return nil, "", false
		}
		return &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: parts[3], Freq: parts[4], DataTime: parts[5]}}}, parts[6], true
	case pb.DataKind_DATA_KIND_RECORD:
		if len(parts) != 5 {
			return nil, "", false
		}
		return &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: parts[2], Version: parts[3]}}}, parts[4], true
	default:
		return nil, "", false
	}
}

func scanBounds(timeRange *pb.TimeRange, versionRange *pb.VersionRange, kind pb.DataKind) (string, string, error) {
	if kind == pb.DataKind_DATA_KIND_TIME_SERIES {
		start, end := "", ""
		if timeRange != nil {
			if timeRange.GetStartTime() != "" {
				at, err := time.Parse(time.RFC3339Nano, timeRange.GetStartTime())
				if err != nil {
					return "", "", invalidf("invalid start_time: %w", err)
				}
				start = at.UTC().Format(canonicalTimeLayout)
			}
			if timeRange.GetEndTime() != "" {
				at, err := time.Parse(time.RFC3339Nano, timeRange.GetEndTime())
				if err != nil {
					return "", "", invalidf("invalid end_time: %w", err)
				}
				end = at.UTC().Format(canonicalTimeLayout)
			}
		}
		if start != "" && end != "" && start > end {
			return "", "", invalid("start_time must not be after end_time")
		}
		return start, end, nil
	}
	start, end := "", ""
	if versionRange != nil {
		start, end = versionRange.GetStartVersion(), versionRange.GetEndVersion()
	}
	if start != "" && end != "" && start > end {
		return "", "", invalid("start_version must not be after end_version")
	}
	return start, end, nil
}

func scanKeyInRange(key *pb.RowKey, start, end string) bool {
	value := ""
	if key != nil {
		if timeSeries := key.GetTimeSeries(); timeSeries != nil {
			value = timeSeries.GetDataTime()
		} else if record := key.GetRecord(); record != nil {
			value = record.GetVersion()
		}
	}
	return (start == "" || value >= start) && (end == "" || value <= end)
}

func scanKeyLess(left, right *pb.RowKey, kind pb.DataKind, order pb.SortOrder) bool {
	leftValue, rightValue := "", ""
	if kind == pb.DataKind_DATA_KIND_TIME_SERIES {
		leftValue, rightValue = left.GetTimeSeries().GetDataTime(), right.GetTimeSeries().GetDataTime()
		if leftValue == rightValue {
			leftValue += "\x00" + left.GetTimeSeries().GetSubjectId() + "\x00" + left.GetTimeSeries().GetFreq()
			rightValue += "\x00" + right.GetTimeSeries().GetSubjectId() + "\x00" + right.GetTimeSeries().GetFreq()
		}
	} else {
		leftValue, rightValue = left.GetRecord().GetVersion(), right.GetRecord().GetVersion()
		if leftValue == rightValue {
			leftValue += "\x00" + left.GetRecord().GetRecordId()
			rightValue += "\x00" + right.GetRecord().GetRecordId()
		}
	}
	if order == pb.SortOrder_SORT_ORDER_DESC {
		return leftValue > rightValue
	}
	return leftValue < rightValue
}

func scanPage(page *pb.Page) (uint, int) {
	pageNo, pageSize := uint(1), 100
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = uint(page.GetPage())
		}
		if page.GetSize() > 0 && page.GetSize() <= 1000 {
			pageSize = int(page.GetSize())
		}
	}
	return pageNo, pageSize
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func unionNames(rows map[string]map[string]struct{}) []string {
	set := make(map[string]struct{})
	for _, names := range rows {
		for name := range names {
			set[name] = struct{}{}
		}
	}
	values := make([]string, 0, len(set))
	for name := range set {
		values = append(values, name)
	}
	sort.Strings(values)
	return values
}

func rowKeyIdentity(key *pb.RowKey) string {
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(key)
	return string(raw)
}
