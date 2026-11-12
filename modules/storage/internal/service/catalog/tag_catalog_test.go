package catalog

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 内置标签只能来自种子导入：带 seed 新建保留 builtin，不带 seed 新建一律非内置，已有标签的 builtin 都不改。
func TestUpsertTagBuiltinOnlyFromSeed(t *testing.T) {
	store, service, _ := newDatasetFixture(t)
	ctx := context.Background()
	upsertWith := func(tagID string, builtin, seed, createOnly bool) *pb.Tag {
		t.Helper()
		rsp, err := service.UpsertTag(ctx, &pb.UpsertTagReq{Seed: seed, CreateOnly: createOnly, Tag: &pb.Tag{
			SpaceId: "factor-space", TagId: tagID, TagName: tagID, Mode: "manual", Builtin: builtin,
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
		got, err := store.GetTag(ctx, "factor-space", tagID)
		require.NoError(t, err)
		return got
	}
	upsert := func(tagID string, builtin, seed bool) *pb.Tag { return upsertWith(tagID, builtin, seed, false) }

	require.True(t, upsert("seeded", true, true).GetBuiltin(), "种子导入新建要保留 builtin")
	require.False(t, upsert("api_created", true, false).GetBuiltin(), "经接口新建的标签不能是内置标签")
	require.True(t, upsert("seeded", false, false).GetBuiltin(), "已有内置标签修改时 builtin 不变")
	require.False(t, upsert("api_created", true, true).GetBuiltin(), "种子导入不改已有标签的 builtin")
	require.True(t, upsertWith("seeded_create_only", true, true, true).GetBuiltin(), "create_only 加 seed 同样保留 builtin")
	require.False(t, upsertWith("api_create_only", true, false, true).GetBuiltin(), "create_only 不带 seed 不是内置标签")
}
