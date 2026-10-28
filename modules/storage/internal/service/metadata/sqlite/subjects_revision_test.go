package sqlite

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func subjectsRevision(t *testing.T, ctx context.Context, store *Store) uint64 {
	t.Helper()
	revision, err := store.SubjectsRevision(ctx, "space")
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func execRevisionSQL(t *testing.T, ctx context.Context, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

// 修订号随标的、标签成员、标签市场类型和数据集标的范围的变化递增，随无关更新保持不变。
func TestSubjectsRevisionTracksSubjectScopeChanges(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	registerActiveNode(t, ctx, store, "node-a")

	expectChange := func(name string, change func()) {
		t.Helper()
		before := subjectsRevision(t, ctx, store)
		change()
		if after := subjectsRevision(t, ctx, store); after <= before {
			t.Fatalf("%s: revision %d -> %d, want increase", name, before, after)
		}
	}
	expectSame := func(name string, change func()) {
		t.Helper()
		before := subjectsRevision(t, ctx, store)
		change()
		if after := subjectsRevision(t, ctx, store); after != before {
			t.Fatalf("%s: revision %d -> %d, want unchanged", name, before, after)
		}
	}

	if _, err := store.UpsertTag(ctx, testAutoTag("assets")); err != nil {
		t.Fatal(err)
	}
	expectChange("insert subject", func() {
		execRevisionSQL(t, ctx, store, `INSERT INTO t_subjects (c_space_id, c_subject_id) VALUES ('space', 'AAA')`)
	})
	expectChange("subject status", func() {
		execRevisionSQL(t, ctx, store, `UPDATE t_subjects SET c_status = 'disabled' WHERE c_subject_id = 'AAA'`)
	})
	expectSame("subject name only", func() {
		execRevisionSQL(t, ctx, store, `UPDATE t_subjects SET c_name = 'renamed' WHERE c_subject_id = 'AAA'`)
	})
	expectChange("subject attrs", func() {
		execRevisionSQL(t, ctx, store, `UPDATE t_subjects SET c_attrs_json = '{"series_tag":"x"}' WHERE c_subject_id = 'AAA'`)
	})
	expectChange("member insert alone", func() {
		execRevisionSQL(t, ctx, store, `INSERT INTO t_subject_tags (c_space_id, c_tag_id, c_subject_id) VALUES ('space', 'assets', 'AAA')`)
	})
	expectChange("member delete alone", func() {
		execRevisionSQL(t, ctx, store, `DELETE FROM t_subject_tags WHERE c_subject_id = 'AAA'`)
	})
	expectChange("tag snapshot adds members", func() {
		if _, err := store.ApplyTagSnapshot(ctx, "space", "assets", []*pb.TagSnapshotItem{
			{SubjectId: "BTC-USDT", Name: "BTC"},
		}); err != nil {
			t.Fatal(err)
		}
	})
	expectChange("member inactive", func() {
		execRevisionSQL(t, ctx, store, `UPDATE t_subject_tags SET c_status = 'inactive' WHERE c_subject_id = 'BTC-USDT'`)
	})
	expectSame("tag description only", func() {
		execRevisionSQL(t, ctx, store, `UPDATE t_tags SET c_description = 'x' WHERE c_tag_id = 'assets'`)
	})
	expectChange("tag attributes", func() {
		execRevisionSQL(t, ctx, store, `UPDATE t_tags SET c_attrs_json = '{"market_type":"swap"}' WHERE c_tag_id = 'assets'`)
	})

	// 数据集：创建、改标的范围、删除后以同 ID 重建，都必须递增（缓存键是 space+dataset）。
	expectChange("dataset insert", func() { createTestDataset(t, ctx, store, "dataset", "node-a") })
	expectChange("dataset subject tags", func() {
		execRevisionSQL(t, ctx, store, `UPDATE t_datasets SET c_subject_tags_json = '["assets"]' WHERE c_dataset_id = 'dataset'`)
	})
	expectSame("dataset description only", func() {
		execRevisionSQL(t, ctx, store, `UPDATE t_datasets SET c_description = 'x' WHERE c_dataset_id = 'dataset'`)
	})
	expectChange("dataset delete", func() {
		if err := store.DeleteDataset(ctx, "space", "dataset"); err != nil {
			t.Fatal(err)
		}
	})
	expectChange("dataset recreate", func() { createTestDataset(t, ctx, store, "dataset", "node-a") })

	// 回滚的事务不改变修订号。
	before := subjectsRevision(t, ctx, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO t_subjects (c_space_id, c_subject_id) VALUES ('space', 'ROLLED')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := subjectsRevision(t, ctx, store); after != before {
		t.Fatalf("rolled back revision %d -> %d", before, after)
	}
}

// 在已有数据的库上重复执行 InitSchema 不报错、不改变数据和修订号。
func TestInitSchemaIsRepeatableOnPopulatedDatabase(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	execRevisionSQL(t, ctx, store, `INSERT INTO t_subjects (c_space_id, c_subject_id) VALUES ('space', 'AAA')`)
	before := subjectsRevision(t, ctx, store)
	for i := 0; i < 2; i++ {
		if err := store.InitSchema(ctx); err != nil {
			t.Fatalf("InitSchema #%d: %v", i+2, err)
		}
	}
	if after := subjectsRevision(t, ctx, store); after != before {
		t.Fatalf("revision changed by InitSchema: %d -> %d", before, after)
	}
	if _, err := store.GetSubject(ctx, "space", "AAA"); err != nil {
		t.Fatalf("subject lost after InitSchema: %v", err)
	}
}

// 缺少新表与触发器的当前版本库重放 InitSchema 后补齐，并从此开始计数。
func TestInitSchemaUpgradesDatabaseWithoutRevisionObjects(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	seedDatasetParents(t, ctx, store)
	execRevisionSQL(t, ctx, store, `INSERT INTO t_subjects (c_space_id, c_subject_id) VALUES ('space', 'AAA')`)
	rows, err := store.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'trg\_%\_rev\_%' ESCAPE '\'`)
	if err != nil {
		t.Fatal(err)
	}
	var triggers []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(triggers) != 11 {
		t.Fatalf("应有 11 个修订号触发器，实际 %d：%v", len(triggers), triggers)
	}
	for _, name := range triggers {
		execRevisionSQL(t, ctx, store, `DROP TRIGGER `+name)
	}
	execRevisionSQL(t, ctx, store, `DROP TABLE t_space_revisions`)

	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("旧库重放 InitSchema: %v", err)
	}
	if got := subjectsRevision(t, ctx, store); got != 0 {
		t.Fatalf("补建后修订号应从 0 开始，实际 %d", got)
	}
	execRevisionSQL(t, ctx, store, `UPDATE t_subjects SET c_status = 'disabled' WHERE c_subject_id = 'AAA'`)
	if got := subjectsRevision(t, ctx, store); got != 1 {
		t.Fatalf("补建后的触发器应生效，实际修订号 %d", got)
	}
}
