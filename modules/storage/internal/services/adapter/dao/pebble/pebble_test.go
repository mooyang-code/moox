package pebble

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mooyang-code/go-commlib/apicache"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

type fieldCache struct {
	fields map[int]*cache.Field
}

func (c fieldCache) GetDataItem(schemaID, searchKey string) any {
	if schemaID != cache.TBField {
		return nil
	}
	fieldID := 0
	if _, err := fmt.Sscanf(searchKey, "field_id=%d", &fieldID); err != nil {
		return nil
	}
	return c.fields[fieldID]
}

func (c fieldCache) GetAll(schemaID string) any {
	if schemaID != cache.TBField {
		return nil
	}
	fields := make([]*cache.Field, 0, len(c.fields))
	for _, field := range c.fields {
		fields = append(fields, field)
	}
	return fields
}

func (c fieldCache) GetKeys(schemaID string) []string {
	if schemaID != cache.TBField {
		return nil
	}
	keys := make([]string, 0, len(c.fields))
	for fieldID := range c.fields {
		keys = append(keys, fmt.Sprintf("field_id=%d", fieldID))
	}
	return keys
}

var _ apicache.ConfigCacher = fieldCache{}

func withFieldCache(t *testing.T, fields map[int]pb.EnumFieldType) {
	t.Helper()

	previous := cache.GetSingeDBCache
	fieldMap := make(map[int]*cache.Field, len(fields))
	for fieldID, fieldType := range fields {
		fieldMap[fieldID] = &cache.Field{
			FieldID:            fieldID,
			FieldPrimaryFormat: int(fieldType),
			Enabled:            "true",
		}
	}

	cache.GetSingeDBCache = func() apicache.ConfigCacher {
		return fieldCache{fields: fieldMap}
	}
	t.Cleanup(func() {
		cache.GetSingeDBCache = previous
	})
}

func stringField(value string) *pb.FieldInfo {
	return &pb.FieldInfo{
		FieldType: pb.EnumFieldType_STR_FIELD,
		SimpleValue: &pb.SimpleValue{
			Value: &pb.SimpleValue_Str{Str: value},
		},
	}
}

func setRow(rowID, times string, fields map[uint32]*pb.FieldInfo) *pb.UpdateDocRow {
	updateFields := make(map[uint32]*pb.UpdateFieldInfo, len(fields))
	for fieldID, fieldInfo := range fields {
		updateFields[fieldID] = &pb.UpdateFieldInfo{
			UpdateType: pb.EnumUpdateType_SET_UPDATE,
			FieldInfo:  fieldInfo,
		}
	}
	return &pb.UpdateDocRow{
		RowId:  rowID,
		Times:  times,
		Fields: updateFields,
	}
}

func TestPebbleAdapter_StaticRowsAndSoftDelete(t *testing.T) {
	withFieldCache(t, map[int]pb.EnumFieldType{
		101: pb.EnumFieldType_STR_FIELD,
	})

	ctx := context.Background()
	store := NewPebble(ctx)
	require.NoError(t, store.GetDeviceConn(t.TempDir()))
	t.Cleanup(func() {
		require.NoError(t, store.CloseDeviceConn())
	})

	require.NoError(t, store.CreateTable(ctx, &dao.CreateTableParams{TableID: "t_profile"}))
	exists, err := store.CheckTable(ctx, "t_profile")
	require.NoError(t, err)
	require.True(t, exists)

	_, err = store.SetFieldInfos(ctx, &dao.SetFieldParams{
		TableID:  "t_profile",
		DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
		UpdateDocRows: []*pb.UpdateDocRow{
			setRow("APT-USDT", "", map[uint32]*pb.FieldInfo{101: stringField("Aptos")}),
		},
	})
	require.NoError(t, err)

	rows, err := store.GetFieldInfos(ctx, &dao.GetFieldParams{
		TableID:  "t_profile",
		DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
		RowID:    "APT-USDT",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "Aptos", rows[0].Fields[101].GetSimpleValue().GetStr())

	deleteRsp, err := store.DeleteRows(ctx, &dao.DeleteRowsParams{
		TableID:  "t_profile",
		DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
		RowIDs:   []string{"APT-USDT"},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), deleteRsp.GetDeletedCount())

	rows, err = store.GetFieldInfos(ctx, &dao.GetFieldParams{
		TableID:  "t_profile",
		DataType: pb.EnumDataTypeCategory_STATIC_DATA_TYPE,
		RowID:    "APT-USDT",
	})
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestPebbleAdapter_TimeSeriesRangeQuery(t *testing.T) {
	withFieldCache(t, map[int]pb.EnumFieldType{
		201: pb.EnumFieldType_STR_FIELD,
	})

	ctx := context.Background()
	store := NewPebble(ctx)
	require.NoError(t, store.GetDeviceConn(t.TempDir()))
	t.Cleanup(func() {
		require.NoError(t, store.CloseDeviceConn())
	})

	_, err := store.SetFieldInfos(ctx, &dao.SetFieldParams{
		TableID:  "t_kline",
		DataType: pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
		UpdateDocRows: []*pb.UpdateDocRow{
			setRow("APT-USDT", "2026-06-13 09:30:00", map[uint32]*pb.FieldInfo{201: stringField("10.1")}),
			setRow("APT-USDT", "2026-06-13 09:31:00", map[uint32]*pb.FieldInfo{201: stringField("10.2")}),
			setRow("APT-USDT", "2026-06-13 09:32:00", map[uint32]*pb.FieldInfo{201: stringField("10.3")}),
		},
		HistoricalRowsLimit: 2,
	})
	require.NoError(t, err)

	rows, err := store.GetFieldInfos(ctx, &dao.GetFieldParams{
		TableID:  "t_kline",
		DataType: pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
		RowID:    "APT-USDT",
		TimeInterval: &pb.TimeInterval{
			Start: "2026-06-13 09:31:00",
			End:   "2026-06-13 09:32:00",
		},
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "2026-06-13 09:31:00", rows[0].Times)
	require.Equal(t, "10.2", rows[0].Fields[201].GetSimpleValue().GetStr())
	require.Equal(t, "2026-06-13 09:32:00", rows[1].Times)
	require.Equal(t, "10.3", rows[1].Fields[201].GetSimpleValue().GetStr())

	require.True(t, strings.Contains(fmt.Sprintf("%T", store.db), "pebble"))
}
