//go:build storage_consistency_contract

package test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	devicepebble "github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestStorageConsistencyContractMergePreservesColumns(t *testing.T) {
	store, err := devicepebble.Open(devicepebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	key := &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "BTC%7C1m%7C", Version: "2026-07-18T00:00:00Z"}
	require.NoError(t, store.WriteRows(context.Background(), []*pb.PrimaryStoreRow{{Key: key, Columns: []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}}))
	require.NoError(t, store.WriteRows(context.Background(), []*pb.PrimaryStoreRow{{Key: key, Columns: []*pb.ColumnValue{{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}}))
	rows, _, err := store.ReadRows(context.Background(), []*pb.PrimaryStoreKey{key}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, rows[0].GetColumns(), 2)
}

func TestStorageConsistencyContractPagination(t *testing.T) {
	store, err := devicepebble.Open(devicepebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	rows := make([]*pb.PrimaryStoreRow, 1001)
	for i := range rows {
		rows[i] = &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{
			SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
			Key: "BTC%7C1m%7C", Version: fmt.Sprintf("2026-07-18T00:00:%02d.%09dZ", i/60, i%60),
		}}
	}
	require.NoError(t, store.WriteRows(context.Background(), rows))
	target := &pb.PrimaryStoreTarget{SpaceId: "crypto", DatasetId: "kline", Engine: "pebble"}
	for _, order := range []pb.SortOrder{pb.SortOrder_SORT_ORDER_ASC, pb.SortOrder_SORT_ORDER_DESC} {
		for _, size := range []uint32{1, 25, 999} {
			var got int
			cursor := ""
			for {
				page, result, err := store.ScanRows(context.Background(), target, pb.DataKind_DATA_KIND_TIME_SERIES, nil, order, nil, &pb.Page{Size: size, Cursor: cursor})
				require.NoError(t, err)
				got += len(page)
				if result == nil || !result.GetHasMore() {
					break
				}
				cursor = result.GetNextCursor()
				require.NotEmpty(t, cursor)
			}
			require.Equal(t, 1001, got, "order=%v size=%d", order, size)
		}
	}
}

func TestStorageConsistencyContractDeleteRemovesFact(t *testing.T) {
	store, err := devicepebble.Open(devicepebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	key := &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "BTC%7C1m%7C", Version: "2026-07-18T00:00:00Z"}
	require.NoError(t, store.WriteRows(context.Background(), []*pb.PrimaryStoreRow{{Key: key}}))
	require.NoError(t, store.DeleteRows(context.Background(), []*pb.PrimaryStoreKey{key}))
	rows, _, err := store.ReadRows(context.Background(), []*pb.PrimaryStoreKey{key}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Empty(t, rows)
}

var _ device.FactStore = (*devicepebble.Store)(nil)
