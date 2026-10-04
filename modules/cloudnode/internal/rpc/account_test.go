package rpc

import (
	"context"
	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	cloudnodeschema "github.com/mooyang-code/moox/modules/cloudnode/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func newCatalogForAccountTests(t *testing.T) *store.CatalogRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(cloudnodeschema.AllSQL()).Error)
	return store.NewCatalogRepository(db)
}

func TestAccountRPC_CRUD(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	ctx := context.Background()

	listRsp, err := svc.ListCloudAccounts(ctx, &pb.ListCloudAccountsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, listRsp.GetRetInfo().GetCode())

	createRsp, err := svc.CreateCloudAccount(ctx, &pb.CreateCloudAccountReq{Account: &pb.CloudAccountInput{
		AccountId: "acct-1", Provider: "tencent", CredentialSecretId: "secret-1",
		AppId: "app", CosRegion: "ap-shanghai", CosBucket: "bucket",
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	assert.Equal(t, "acct-1", createRsp.GetAccount().GetAccountId())

	updateRsp, err := svc.UpdateCloudAccount(ctx, &pb.UpdateCloudAccountReq{Account: &pb.CloudAccountInput{
		AccountId: "acct-1", Provider: "tencent", CredentialSecretId: "secret-1", CosRegion: "ap-guangzhou",
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, updateRsp.GetRetInfo().GetCode())

	deleteRsp, err := svc.DeleteCloudAccount(ctx, &pb.DeleteCloudAccountReq{AccountId: "acct-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, deleteRsp.GetRetInfo().GetCode())
}

func TestPackageRPC_RequiresSpaceContext(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	rsp, err := svc.GetPackageList(context.Background(), &pb.GetPackageListReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp2, err := svc.GetPackageList(spacecontext.WithSpaceID(context.Background(), "crypto"), &pb.GetPackageListReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp2.GetRetInfo().GetCode())
}

func TestAccountRPC_ValidatesRequiredFields(t *testing.T) {
	svc := &Service{catalog: newCatalogForAccountTests(t)}
	rsp, err := svc.CreateCloudAccount(context.Background(), &pb.CreateCloudAccountReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
