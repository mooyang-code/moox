package space

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/control/internal/config"
	"github.com/mooyang-code/moox/modules/control/internal/service/database"
	"github.com/stretchr/testify/require"
)

func TestServiceCreatesListsAndUpdatesSpaces(t *testing.T) {
	manager := newTestManager(t)
	service := NewService(manager)
	ctx := context.Background()

	created, err := service.CreateSpace(ctx, &Space{
		SpaceID:     "cn-stock",
		Name:        "A股交易空间",
		Description: "A股行情、因子、策略与交易隔离空间",
		Owner:       "alice",
		Market:      "CN",
		Timezone:    "Asia/Shanghai",
		Attributes:  `{"risk":"low"}`,
	})
	require.NoError(t, err)
	require.Equal(t, "active", created.Status)
	require.NotZero(t, created.ID)

	var rowCount int64
	require.NoError(t, manager.GetDB().Table("t_spaces").Where("c_space_id = ? AND c_invalid = 0", "cn-stock").Count(&rowCount).Error)
	require.Equal(t, int64(1), rowCount)

	spaces, page, err := service.ListSpaces(ctx, "alice", "active", PageReq{Page: 1, Size: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.False(t, page.HasMore)
	require.Len(t, spaces, 1)
	require.Equal(t, "cn-stock", spaces[0].SpaceID)
	require.Equal(t, "A股交易空间", spaces[0].Name)

	updated, err := service.UpdateSpace(ctx, &Space{
		SpaceID:     "cn-stock",
		Name:        "A股投研空间",
		Description: "更新后的空间说明",
		Owner:       "bob",
		Market:      "CN-A",
		Timezone:    "Asia/Shanghai",
		Status:      "paused",
		Attributes:  `{"tier":"prod"}`,
	})
	require.NoError(t, err)
	require.Equal(t, "A股投研空间", updated.Name)

	spaces, page, err = service.ListSpaces(ctx, "bob", "paused", PageReq{Page: 1, Size: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, spaces, 1)
	require.Equal(t, "CN-A", spaces[0].Market)
	require.Equal(t, `{"tier":"prod"}`, spaces[0].Attributes)
}

func TestServiceListsSpaceMembers(t *testing.T) {
	manager := newTestManager(t)
	service := NewService(manager)
	ctx := context.Background()

	require.NoError(t, manager.GetDB().Exec(`
		INSERT INTO t_space_members (c_space_id, c_user_id, c_role, c_status, c_attributes)
		VALUES
			('cn-stock', 'alice', 'owner', 'active', '{}'),
			('cn-stock', 'bob', 'member', 'active', '{}'),
			('us-stock', 'carol', 'owner', 'active', '{}')
	`).Error)

	members, page, err := service.ListSpaceMembers(ctx, "cn-stock", PageReq{Page: 1, Size: 10})
	require.NoError(t, err)
	require.Equal(t, int64(2), page.Total)
	require.Len(t, members, 2)
	require.ElementsMatch(t, []string{"alice", "bob"}, []string{members[0].UserID, members[1].UserID})
}

func TestGatewayHandlerForwardsSpaceMethods(t *testing.T) {
	manager := newTestManager(t)
	handler := &gatewayHandler{service: NewService(manager)}
	ctx := context.Background()

	createBody, err := json.Marshal(map[string]interface{}{
		"space": map[string]interface{}{
			"space_id": "crypto",
			"name":     "Crypto 交易空间",
			"owner":    "satoshi",
			"market":   "Crypto",
			"timezone": "UTC",
		},
	})
	require.NoError(t, err)

	body, err := handler.ForwardRequest(ctx, "CreateSpace", nil, createBody)
	require.NoError(t, err)
	var createResp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Space   Space  `json:"space"`
	}
	require.NoError(t, json.Unmarshal(body, &createResp))
	require.Equal(t, 0, createResp.Code)
	require.Equal(t, "success", createResp.Message)
	require.Equal(t, "crypto", createResp.Space.SpaceID)

	listBody, err := json.Marshal(map[string]interface{}{
		"owner": "satoshi",
		"page":  map[string]int{"page": 1, "size": 10},
	})
	require.NoError(t, err)

	body, err = handler.ForwardRequest(ctx, "ListSpaces", nil, listBody)
	require.NoError(t, err)
	var listResp struct {
		Code       int        `json:"code"`
		Message    string     `json:"message"`
		Spaces     []Space    `json:"spaces"`
		PageResult PageResult `json:"page_result"`
	}
	require.NoError(t, json.Unmarshal(body, &listResp))
	require.Equal(t, 0, listResp.Code)
	require.Equal(t, int64(1), listResp.PageResult.Total)
	require.Len(t, listResp.Spaces, 1)
	require.Equal(t, "crypto", listResp.Spaces[0].SpaceID)

	_, err = handler.ForwardRequest(ctx, "Unknown", nil, nil)
	require.Error(t, err)
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
