package space

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
)

func TestServiceCreatesListsAndUpdatesSpaces(t *testing.T) {
	manager := newTestManager(t)
	svc := NewService(manager)
	ctx := context.Background()

	createRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId:     "cn-stock",
		Name:        "A股交易空间",
		Description: "A股行情、因子、策略与交易隔离空间",
		Owner:       "alice",
		Market:      "CN",
		Timezone:    "Asia/Shanghai",
		Attributes:  `{"risk":"low"}`,
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	require.Equal(t, "active", createRsp.GetSpace().GetStatus())
	require.NotZero(t, createRsp.GetSpace().GetId())

	var rowCount int64
	require.NoError(t, manager.GetDB().Table("t_spaces").Where("c_space_id = ? AND c_is_deleted != 'true'", "cn-stock").Count(&rowCount).Error)
	require.Equal(t, int64(1), rowCount)

	listRsp, err := svc.ListSpaces(ctx, &pb.ListSpacesReq{
		Owner:  "alice",
		Status: "active",
		Page:   &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), listRsp.GetPageResult().GetTotal())
	require.False(t, listRsp.GetPageResult().GetHasMore())
	require.Len(t, listRsp.GetSpaces(), 1)
	require.Equal(t, "cn-stock", listRsp.GetSpaces()[0].GetSpaceId())
	require.Equal(t, "A股交易空间", listRsp.GetSpaces()[0].GetName())

	updateRsp, err := svc.UpdateSpace(ctx, &pb.UpdateSpaceReq{Space: &pb.Space{
		SpaceId:     "cn-stock",
		Name:        "A股投研空间",
		Description: "更新后的空间说明",
		Owner:       "bob",
		Market:      "CN-A",
		Timezone:    "Asia/Shanghai",
		Status:      "paused",
		Attributes:  `{"tier":"prod"}`,
	}})
	require.NoError(t, err)
	require.Equal(t, "A股投研空间", updateRsp.GetSpace().GetName())

	listRsp, err = svc.ListSpaces(ctx, &pb.ListSpacesReq{
		Owner:  "bob",
		Status: "paused",
		Page:   &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), listRsp.GetPageResult().GetTotal())
	require.Len(t, listRsp.GetSpaces(), 1)
	require.Equal(t, "CN-A", listRsp.GetSpaces()[0].GetMarket())
	require.Equal(t, `{"tier":"prod"}`, listRsp.GetSpaces()[0].GetAttributes())
}

func TestServiceCreateSpaceDuplicateReturnsFriendlyError(t *testing.T) {
	manager := newTestManager(t)
	svc := NewService(manager)
	ctx := context.Background()

	_, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "cn-stock", Name: "A股交易空间"}})
	require.NoError(t, err)

	_, err = svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "cn-stock", Name: "重复空间"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "space_id already exists: cn-stock")
	require.NotContains(t, err.Error(), "UNIQUE constraint")
}

func TestServiceListsSpaceMembers(t *testing.T) {
	manager := newTestManager(t)
	svc := NewService(manager)
	ctx := context.Background()

	require.NoError(t, manager.GetDB().Exec(`
		INSERT INTO t_space_members (c_space_id, c_user_id, c_role, c_status, c_attributes)
		VALUES
			('cn-stock', 'alice', 'owner', 'active', '{}'),
			('cn-stock', 'bob', 'member', 'active', '{}'),
			('us-stock', 'carol', 'owner', 'active', '{}')
	`).Error)

	rsp, err := svc.ListSpaceMembers(ctx, &pb.ListSpaceMembersReq{
		SpaceId: "cn-stock",
		Page:    &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(2), rsp.GetPageResult().GetTotal())
	require.Len(t, rsp.GetMembers(), 2)
	require.ElementsMatch(t, []string{"alice", "bob"}, []string{rsp.GetMembers()[0].GetUserId(), rsp.GetMembers()[1].GetUserId()})
}

func newTestManager(t *testing.T) *database.Manager {
	t.Helper()

	manager := database.NewManager()
	require.NoError(t, manager.Initialize(&config.DatabaseConfig{Path: t.TempDir() + "/moox.db"}))
	t.Cleanup(func() {
		require.NoError(t, manager.Close())
	})
	return manager
}
