package pebble

import (
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/services/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

const keyPrefix = "r"

func encodeRowKey(row *pb.DataRow) string {
	key := row.GetKey()
	scope := key.GetScope()
	rowID := key.GetRowId()
	if rowID == "" {
		rowID = "_"
	}
	dataTime := key.GetDataTime()
	if dataTime == "" {
		dataTime = "_"
	}
	return strings.Join([]string{
		keyPrefix,
		escape(scope.GetSpaceId()),
		escape(scope.GetDatasetId()),
		escape(scope.GetSubjectId()),
		escape(scope.GetFreq()),
		escape(factkey.DimensionsHash(scope.GetDimensions())),
		escape(dataTime),
		escape(rowID),
	}, "|")
}

func encodeScopePrefix(scope *pb.DataScope) string {
	parts := []string{keyPrefix, escape(scope.GetSpaceId()), escape(scope.GetDatasetId())}
	if scope.GetSubjectId() != "" {
		parts = append(parts, escape(scope.GetSubjectId()))
	}
	return strings.Join(parts, "|") + "|"
}

func escape(value string) string {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "|", "%7C")
	return value
}
