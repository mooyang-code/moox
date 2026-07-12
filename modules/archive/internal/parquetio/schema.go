package parquetio

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/parquet-go/parquet-go"
)

const (
	colCandleTime = "candle_begin_time"
	colSpace      = "space_id"
	colDataset    = "dataset_id"
	colSubject    = "subject_id"
	colFreq       = "freq"
	colDimensions = "dimensions_json"
	colAttributes = "attributes_json"
	colWrittenAt  = "written_at"
)

func BuildSchema(columns map[string]storagepb.FieldValueType) (*parquet.Schema, error) {
	group := parquet.Group{colCandleTime: parquet.Timestamp(parquet.Nanosecond), colSpace: parquet.String(), colDataset: parquet.String(), colSubject: parquet.String(), colFreq: parquet.String(), colDimensions: parquet.String(), colAttributes: parquet.String(), colWrittenAt: parquet.Timestamp(parquet.Nanosecond)}
	for _, name := range domain.SortedColumnNames(columns) {
		node, err := businessNode(columns[name])
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
		group[name] = parquet.Optional(node)
	}
	return parquet.NewSchema("moox_archive_v1", group), nil
}

func businessNode(kind storagepb.FieldValueType) (parquet.Node, error) {
	switch kind {
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return parquet.String(), nil
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return parquet.Int(64), nil
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return parquet.Leaf(parquet.DoubleType), nil
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return parquet.Leaf(parquet.BooleanType), nil
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return parquet.Timestamp(parquet.Nanosecond), nil
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return parquet.Leaf(parquet.ByteArrayType), nil
	default:
		return nil, fmt.Errorf("unsupported field value type %s", kind.String())
	}
}
