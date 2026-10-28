package pebble

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// upsertTestRowsEntries 按数据集分组写入测试行（不带 commit_id），返回各次写入产生的 outbox 条目。
func upsertTestRowsEntries(store *Store, ctx context.Context, rows []*pb.RowFieldUpsert) ([]*OutboxEntry, error) {
	order := make([][2]string, 0)
	groups := make(map[[2]string][]*pb.RowFieldUpsert)
	for _, row := range rows {
		key := [2]string{row.GetKey().GetSpaceId(), row.GetKey().GetDatasetId()}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	var all []*OutboxEntry
	for _, key := range order {
		entries, err := store.UpsertFields(ctx, key[0], key[1], "", groups[key])
		if err != nil {
			return all, err
		}
		all = append(all, entries...)
	}
	return all, nil
}

// upsertTestRows 是只关心错误的 upsertTestRowsEntries。
func upsertTestRows(store *Store, ctx context.Context, rows []*pb.RowFieldUpsert) error {
	_, err := upsertTestRowsEntries(store, ctx, rows)
	return err
}
