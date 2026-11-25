package sqlite

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 空间、数据集写进空库再读回：created_at、updated_at 是非零毫秒，创建返回值与读回值相同（查询索引按它认数据集）。
func TestMetadataTimestampsRoundTripAsMillis(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	// 带亚毫秒部分：库里存 RFC3339Nano 文本，读回截到毫秒，必须与创建时返回的毫秒数一致。
	createTime := time.Date(2026, time.November, 24, 8, 30, 15, 123456789, time.UTC)
	store.now = func() time.Time { return createTime }

	space, err := store.CreateSpace(ctx, &pb.Space{SpaceId: "space", Name: "Space"})
	if err != nil {
		t.Fatal(err)
	}
	if space.GetCreatedAt() != createTime.UnixMilli() || space.GetUpdatedAt() != createTime.UnixMilli() {
		t.Fatalf("CreateSpace 返回 %d/%d，期望 %d", space.GetCreatedAt(), space.GetUpdatedAt(), createTime.UnixMilli())
	}
	gotSpace, err := store.GetSpace(ctx, "space")
	if err != nil {
		t.Fatal(err)
	}
	if gotSpace.GetCreatedAt() != space.GetCreatedAt() || gotSpace.GetUpdatedAt() != space.GetUpdatedAt() {
		t.Fatalf("GetSpace 读回 %d/%d，创建返回 %d/%d", gotSpace.GetCreatedAt(), gotSpace.GetUpdatedAt(), space.GetCreatedAt(), space.GetUpdatedAt())
	}
	listed, _, err := store.ListSpaces(ctx, "", "", nil)
	if err != nil || len(listed) != 1 || listed[0].GetCreatedAt() != space.GetCreatedAt() {
		t.Fatalf("ListSpaces = %v, %v", listed, err)
	}
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "space", DataSourceId: "source", Name: "source"}); err != nil {
		t.Fatal(err)
	}

	created := createTestDataset(t, ctx, store, "dataset")
	if created.GetCreatedAt() != createTime.UnixMilli() || created.GetUpdatedAt() != createTime.UnixMilli() {
		t.Fatalf("CreateDataset 返回 %d/%d，期望 %d", created.GetCreatedAt(), created.GetUpdatedAt(), createTime.UnixMilli())
	}
	got, err := store.GetDataset(ctx, "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetCreatedAt() != created.GetCreatedAt() || got.GetUpdatedAt() != created.GetUpdatedAt() {
		t.Fatalf("GetDataset 读回 %d/%d，创建返回 %d/%d", got.GetCreatedAt(), got.GetUpdatedAt(), created.GetCreatedAt(), created.GetUpdatedAt())
	}

	updateTime := createTime.Add(90*time.Second + 654321*time.Nanosecond)
	store.now = func() time.Time { return updateTime }
	updated, err := store.UpdateDataset(ctx, &pb.Dataset{SpaceId: "space", DatasetId: "dataset", Name: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = store.GetDataset(ctx, "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetCreatedAt() != created.GetCreatedAt() || got.GetUpdatedAt() != updateTime.UnixMilli() || updated.GetUpdatedAt() != got.GetUpdatedAt() {
		t.Fatalf("更新后读回 %d/%d（返回 updated_at=%d），期望 created_at=%d updated_at=%d", got.GetCreatedAt(), got.GetUpdatedAt(), updated.GetUpdatedAt(), created.GetCreatedAt(), updateTime.UnixMilli())
	}
}

// 库列由 CURRENT_TIMESTAMP 默认值写入（秒精度、无时区）时也换成毫秒回填。
func TestUpsertSpaceReadsDefaultTimestampAsMillis(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	before := time.Now().Add(-2 * time.Second).UnixMilli()
	space, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "space", Name: "Space"})
	if err != nil {
		t.Fatal(err)
	}
	if space.GetCreatedAt() < before || space.GetUpdatedAt() < before {
		t.Fatalf("UpsertSpace 读回 %d/%d，应不早于 %d", space.GetCreatedAt(), space.GetUpdatedAt(), before)
	}
}

// 数据来源的 created_at、updated_at 按库列回填：写入、单查、列表三处都不是 0。
func TestDataSourceReadsTimestampsFromColumns(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)
	before := time.Now().Add(-2 * time.Second).UnixMilli()
	if _, err := store.CreateSpace(ctx, &pb.Space{SpaceId: "space", Name: "Space"}); err != nil {
		t.Fatal(err)
	}
	upserted, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "space", DataSourceId: "source", Name: "source"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDataSource(ctx, "space", "source")
	if err != nil {
		t.Fatal(err)
	}
	listed, _, err := store.ListDataSources(ctx, "space", "", nil)
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListDataSources = %v, %v", listed, err)
	}
	for name, item := range map[string]*pb.DataSource{"UpsertDataSource": upserted, "GetDataSource": got, "ListDataSources": listed[0]} {
		if item.GetCreatedAt() < before || item.GetUpdatedAt() < before {
			t.Fatalf("%s 读回 %d/%d，应不早于 %d", name, item.GetCreatedAt(), item.GetUpdatedAt(), before)
		}
	}
}

func TestSetTimestampFieldByKind(t *testing.T) {
	const text = "2026-11-24 08:30:15"
	want := time.Date(2026, time.November, 24, 8, 30, 15, 0, time.UTC).UnixMilli()

	dataset := &pb.Dataset{}
	if err := setTimestampField(dataset, "created_at", text); err != nil || dataset.GetCreatedAt() != want {
		t.Fatalf("int64 字段回填 = %d, %v", dataset.GetCreatedAt(), err)
	}
	// string 字段写 RFC3339Nano 文本。
	if err := setTimestampField(dataset, "name", text); err != nil || dataset.GetName() != "2026-11-24T08:30:15Z" {
		t.Fatalf("string 字段回填 = %q, %v", dataset.GetName(), err)
	}
	// 空文本不回填。
	dataset.CreatedAt = 7
	if err := setTimestampField(dataset, "created_at", ""); err != nil || dataset.GetCreatedAt() != 7 {
		t.Fatalf("空文本 = %d, %v", dataset.GetCreatedAt(), err)
	}
	// 既不是 string 也不是 int64 的字段、不存在的字段、解析不了的文本都报错，不静默跳过。
	for name, call := range map[string]func() error{
		"uint32 字段": func() error { return setTimestampField(dataset, "maintenance_priority", text) },
		"不存在的字段":    func() error { return setTimestampField(dataset, "missing_at", text) },
		"解析不了的文本":   func() error { return setTimestampField(dataset, "created_at", "yesterday") },
	} {
		if err := call(); err == nil {
			t.Fatalf("%s 应报错，得到 %v", name, err)
		}
	}
}
