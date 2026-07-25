package store

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	cloudnodeschema "github.com/mooyang-code/moox/modules/cloudnode/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestCatalog(t *testing.T) *CatalogRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(cloudnodeschema.AllSQL()).Error)
	return NewCatalogRepository(db)
}

func TestCatalogRepository_AccountCRUD(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	require.NoError(t, repo.UpsertAccount(ctx, CloudAccount{
		AccountID: "acct-1", AccountName: "main", Provider: "tencent",
		CredentialSecretID: "secret-1", AppID: "app",
	}))
	got, err := repo.GetAccount(ctx, "acct-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "main", got.AccountName)

	accounts, total, err := repo.ListAccounts(ctx, "tencent")
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, accounts, 1)

	require.NoError(t, repo.DeleteAccount(ctx, "acct-1"))
	missing, err := repo.GetAccount(ctx, "acct-1")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestCatalogRepository_NodeLifecycle(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	require.NoError(t, repo.UpsertNode(ctx, CloudNode{
		SpaceID: "crypto", NodeID: "node-a", CloudAccountID: "acct-1",
		Region: "ap-guangzhou", Namespace: "default", Status: "online",
		SupportedWorkloads: `["collect.kline"]`, DeploymentID: "dep-1",
	}))
	node, err := repo.GetNode(ctx, "crypto", "node-a")
	require.NoError(t, err)
	require.NotNil(t, node)
	assert.Equal(t, "tencent-scf", node.Provider)

	nodes, total, err := repo.ListNodes(ctx, "crypto", &pb.GetNodeListReq{
		NodeId: "node", Region: "ap-guangzhou", Status: pb.NodeStatusCode_NODE_STATUS_ONLINE,
		Keyword: "node", Page: &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, nodes, 1)

	matched, err := repo.FindNodeForInvocation(ctx, "crypto", "dep-1", "collect.kline")
	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "node-a", matched.NodeID)

	require.NoError(t, repo.UpdateNodeDeployment(ctx, "crypto", "node-a", "pkg-1", "v2"))
	require.NoError(t, repo.UpsertHeartbeat(ctx, "crypto", "node-a", "scf", "v2", `["collect.kline"]`, `{}`))
	require.NoError(t, repo.DeleteNodes(ctx, "crypto", []string{"node-a"}))
	deleted, err := repo.GetNode(ctx, "crypto", "node-a")
	require.NoError(t, err)
	assert.Nil(t, deleted)
}

func TestPageFromCommon_NormalizesBounds(t *testing.T) {
	page, size := pageFromCommon(nil)
	assert.Equal(t, 1, page)
	assert.Equal(t, 50, size)
	page, size = pageFromCommon(&pb.Page{Page: 0, Size: 5000})
	assert.Equal(t, 1, page)
	assert.Equal(t, 1000, size)
}

func TestFindNodeForInvocation_RequiresSpaceID(t *testing.T) {
	repo := newTestCatalog(t)
	_, err := repo.FindNodeForInvocation(context.Background(), "", "", "")
	assert.Error(t, err)
}

func TestCatalogRepository_PackageCRUD(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	require.NoError(t, repo.UpsertPackage(ctx, FunctionPackage{
		SpaceID: "crypto", PackageID: "pkg-1", PackageName: "collector",
		Version: "v1", Runtime: "CustomRuntime", Status: "available",
		PackageType: "collector", WorkloadType: "collect.kline",
		COSBucket: "bucket", COSPath: "/path/pkg.zip",
	}))
	pkg, err := repo.GetPackage(ctx, "crypto", "pkg-1")
	require.NoError(t, err)
	require.NotNil(t, pkg)
	assert.Equal(t, "v1", pkg.Version)

	pkgs, total, err := repo.ListPackages(ctx, "crypto", &pb.GetPackageListReq{
		Runtime: "CustomRuntime", Page: &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, pkgs, 1)

	require.NoError(t, repo.DeletePackage(ctx, "crypto", "pkg-1"))
	deleted, err := repo.GetPackage(ctx, "crypto", "pkg-1")
	require.NoError(t, err)
	assert.Nil(t, deleted)
}
