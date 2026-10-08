package storageio

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"google.golang.org/protobuf/proto"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (c *Client) WriteRows(ctx context.Context, spaceID, datasetID, commitID string, rows []ResultRow) error {
	if err := c.primaryReady("write factor rows"); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(commitID) == "" || strings.TrimSpace(commitID) != commitID {
		return errors.New("space_id, dataset_id and commit_id are required")
	}
	info, err := c.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	frequency := info.Freq
	if frequency == "" {
		return fmt.Errorf("factor result dataset %q has no frequency", datasetID)
	}
	upserts := make([]*storagepb.RowFieldUpsert, 0, len(rows))
	for index, row := range rows {
		if strings.TrimSpace(row.SubjectID) == "" || row.DataTime.IsZero() {
			return fmt.Errorf("result row %d requires subject_id and data_time", index)
		}
		if len(row.Fields) == 0 {
			return fmt.Errorf("result row %d requires at least one field", index)
		}
		fieldNames := make([]string, 0, len(row.Fields))
		for name := range row.Fields {
			if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
				return fmt.Errorf("result row %d contains an empty field name", index)
			}
			fieldNames = append(fieldNames, name)
		}
		sort.Strings(fieldNames)
		fields := make([]*storagepb.FieldValue, 0, len(fieldNames))
		for _, name := range fieldNames {
			value, valueErr := encodeValue(row.Fields[name])
			if valueErr != nil {
				return fmt.Errorf("encode result row %d field %q: %w", index, name, valueErr)
			}
			fields = append(fields, &storagepb.FieldValue{FieldId: name, Value: value})
		}
		upserts = append(upserts, &storagepb.RowFieldUpsert{
			Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID,
				Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
					SubjectId: row.SubjectID, Freq: frequency,
					DataTime: row.DataTime.UTC().Format(time.RFC3339Nano), SeriesTag: row.SeriesTag,
				}},
			},
			Fields: fields,
		})
	}
	rsp, err := c.primary.WriteFactorRows(ctx, &storagepb.PrimaryWriteFactorRowsReq{
		AuthInfo: c.auth, SpaceId: spaceID, DatasetId: datasetID, CommitId: commitID, Rows: upserts,
	})
	if err != nil {
		return rpcError("write factor rows", err)
	}
	if rsp == nil {
		return fmt.Errorf("%w: write factor rows returned an empty response", ErrInfra)
	}
	if err := responseError("write factor rows", rsp.GetRetInfo()); err != nil {
		return err
	}
	if rsp.GetRowsWritten() != uint64(len(rows)) {
		return fmt.Errorf("%w: Storage wrote %d of %d factor rows", ErrInfra, rsp.GetRowsWritten(), len(rows))
	}
	return nil
}

func encodeValue(value any) (*storagepb.TypedValue, error) {
	if value == nil {
		return nullValue(), nil
	}
	switch item := value.(type) {
	case string:
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: item}}, nil
	case bool:
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: item}}, nil
	case int:
		return intValue(int64(item)), nil
	case int8:
		return intValue(int64(item)), nil
	case int16:
		return intValue(int64(item)), nil
	case int32:
		return intValue(int64(item)), nil
	case int64:
		return intValue(item), nil
	case uint:
		return unsignedValue(uint64(item))
	case uint8:
		return intValue(int64(item)), nil
	case uint16:
		return intValue(int64(item)), nil
	case uint32:
		return intValue(int64(item)), nil
	case uint64:
		return unsignedValue(item)
	case float32:
		return doubleValue(float64(item)), nil
	case float64:
		return doubleValue(item), nil
	case time.Time:
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_TimeValue{TimeValue: item.UTC().Format(time.RFC3339Nano)}}, nil
	case json.RawMessage:
		if !json.Valid(item) {
			return nil, errors.New("invalid JSON field value")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, item); err != nil {
			return nil, fmt.Errorf("compact JSON field value: %w", err)
		}
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_JsonValue{JsonValue: compact.String()}}, nil
	case []byte:
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_BytesValue{BytesValue: append([]byte(nil), item...)}}, nil
	case json.Number:
		if integer, err := item.Int64(); err == nil {
			return intValue(integer), nil
		}
		if decimal, err := item.Float64(); err == nil {
			return doubleValue(decimal), nil
		}
		return nil, fmt.Errorf("invalid JSON number %q", item)
	}
	if reflect.ValueOf(value).Kind() == reflect.Map || reflect.ValueOf(value).Kind() == reflect.Slice || reflect.ValueOf(value).Kind() == reflect.Array {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("marshal structured value: %w", err)
		}
		return &storagepb.TypedValue{Value: &storagepb.TypedValue_JsonValue{JsonValue: string(encoded)}}, nil
	}
	return nil, fmt.Errorf("unsupported field value type %T", value)
}

func intValue(value int64) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: value}}
}

func unsignedValue(value uint64) (*storagepb.TypedValue, error) {
	if value > math.MaxInt64 {
		return nil, fmt.Errorf("unsigned integer %d exceeds Storage int range", value)
	}
	return intValue(int64(value)), nil
}

func doubleValue(value float64) *storagepb.TypedValue {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nullValue()
	}
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}
}

func nullValue() *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}}
}

func canonicalValue(value any) string {
	encoded, err := encodeValue(value)
	if err == nil {
		payload, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(encoded)
		if marshalErr == nil {
			return hex.EncodeToString(payload)
		}
	}
	return fmt.Sprintf("%T:<invalid>", value)
}
