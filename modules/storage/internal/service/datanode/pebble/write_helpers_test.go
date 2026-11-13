package pebble

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// upsertTestRows 按数据集分组写入测试行（不带 commit_id）。
func upsertTestRows(store *Store, ctx context.Context, rows []*pb.RowFieldUpsert) error {
	order := make([][2]string, 0)
	groups := make(map[[2]string][]*pb.RowFieldUpsert)
	for _, row := range rows {
		key := [2]string{row.GetKey().GetSpaceId(), row.GetKey().GetDatasetId()}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	for _, key := range order {
		if _, err := store.UpsertFields(ctx, key[0], key[1], "", groups[key]); err != nil {
			return err
		}
	}
	return nil
}
