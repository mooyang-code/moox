package space

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSpaceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return db
}

func newTestService(t *testing.T) Service {
	return &service{dao: NewDAO(setupSpaceTestDB(t))}
}

func TestDAO_CreateSpace_DuplicateID_ShouldError(t *testing.T) {
	d := NewDAO(setupSpaceTestDB(t))
	item := &Space{SpaceID: "cn-a", Name: "A股"}
	require.NoError(t, d.CreateSpace(context.Background(), item))
	err := d.CreateSpace(context.Background(), &Space{SpaceID: "cn-a", Name: "重复"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestDAO_UpdateSpace_NotFound_ShouldError(t *testing.T) {
	d := NewDAO(setupSpaceTestDB(t))
	err := d.UpdateSpace(context.Background(), &Space{SpaceID: "missing", Name: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDAO_ListSpaces_WithFilter_ShouldReturnMatches(t *testing.T) {
	d := NewDAO(setupSpaceTestDB(t))
	require.NoError(t, d.CreateSpace(context.Background(), &Space{
		SpaceID: "us", Name: "美股", Owner: "alice", Status: "active",
	}))
	rows, total, err := d.ListSpaces(context.Background(), "alice", "active", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "us", rows[0].SpaceID)
}

func TestDAO_ListSpaceMembers_EmptySpace_ShouldReturnZero(t *testing.T) {
	d := NewDAO(setupSpaceTestDB(t))
	rows, total, err := d.ListSpaceMembers(context.Background(), "empty", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
	assert.Empty(t, rows)
}

func TestDAO_AuthorizeTradeRequest_RequiresActiveMembershipForOrdinaryUser(t *testing.T) {
	db := setupSpaceTestDB(t)
	d := NewDAO(db)
	require.NoError(t, d.CreateSpace(context.Background(), &Space{SpaceID: "crypto", Name: "Crypto", Status: "active"}))
	err := d.AuthorizeTradeRequest(context.Background(), "user-1", "crypto", "ListOrders", 1)
	assert.Error(t, err)
	require.NoError(t, db.Create(&SpaceMember{SpaceID: "crypto", UserID: "user-1", Role: "member", Status: "active"}).Error)
	require.NoError(t, d.AuthorizeTradeRequest(context.Background(), "user-1", "crypto", "ListOrders", 1))
	assert.Error(t, d.AuthorizeTradeRequest(context.Background(), "user-1", "crypto", "PlaceManualOrder", 1))
}

func TestDAO_AuthorizeTradeRequest_GlobalAdminAndInactiveSpace(t *testing.T) {
	db := setupSpaceTestDB(t)
	d := NewDAO(db)
	require.NoError(t, d.CreateSpace(context.Background(), &Space{SpaceID: "crypto", Name: "Crypto", Status: "active"}))
	require.NoError(t, d.AuthorizeTradeRequest(context.Background(), "admin-1", "crypto", "PlaceManualOrder", 2))
	require.NoError(t, db.Model(&Space{}).Where("c_space_id = ?", "crypto").Update("c_status", "disabled").Error)
	assert.Error(t, d.AuthorizeTradeRequest(context.Background(), "admin-1", "crypto", "ListOrders", 2))
}

func TestService_CreateSpace_MissingRequired_ShouldError(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{
		Space: &pb.Space{SpaceId: "", Name: ""},
	})
	require.Error(t, err)
}

func TestService_CreateSpace_ValidRequest_ShouldPersist(t *testing.T) {
	svc := newTestService(t)
	rsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{
		Space: &pb.Space{SpaceId: "crypto", Name: "Crypto", Owner: "ops"},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "crypto", rsp.GetSpace().GetSpaceId())
}

func TestService_ListSpaces_DefaultPage_ShouldReturnCreated(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{
		Space: &pb.Space{SpaceId: "hk", Name: "港股"},
	})
	require.NoError(t, err)

	rsp, err := svc.ListSpaces(context.Background(), &pb.ListSpacesReq{Page: &pb.Page{}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetSpaces(), 1)
}

func TestService_UpdateSpace_ValidRequest_ShouldPersist(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{
		Space: &pb.Space{SpaceId: "crypto", Name: "Crypto"},
	})
	require.NoError(t, err)

	rsp, err := svc.UpdateSpace(context.Background(), &pb.UpdateSpaceReq{
		Space: &pb.Space{SpaceId: "crypto", Name: "Crypto Updated", Status: "active"},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "Crypto Updated", rsp.GetSpace().GetName())
}

func TestService_ListSpaceMembers_MissingSpaceID_ShouldError(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.ListSpaceMembers(context.Background(), &pb.ListSpaceMembersReq{})
	require.Error(t, err)
}

func TestNormalizePage_InvalidValues_ShouldUseDefaults(t *testing.T) {
	pageNo, offset, size := normalizePage(&pb.Page{Page: 0, Size: 0})
	assert.Equal(t, 1, pageNo)
	assert.Equal(t, 0, offset)
	assert.Equal(t, 20, size)
}

func TestNormalizePage_Oversize_ShouldResetToDefault(t *testing.T) {
	_, _, size := normalizePage(&pb.Page{Page: 1, Size: 500})
	assert.Equal(t, 20, size)
}
