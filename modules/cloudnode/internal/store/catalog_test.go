package store

import (
	"context"
	"fmt"
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
		NodeType: "scf-event", Region: "ap-guangzhou", Namespace: "default", Status: "online",
		SupportedWorkloads: `["collect.kline"]`, DeploymentID: "dep-1",
		LastHeartbeatAt: timePtr(repo.currentTime()),
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
	require.NoError(t, repo.UpdateHeartbeat(ctx, "crypto", "node-a", "v2", `["collect.kline"]`, `{}`))
	updated, err := repo.GetNode(ctx, "crypto", "node-a")
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "scf-event", updated.NodeType)
	require.NoError(t, repo.DeleteNodes(ctx, "crypto", []string{"node-a"}))
	deleted, err := repo.GetNode(ctx, "crypto", "node-a")
	require.NoError(t, err)
	assert.Nil(t, deleted)
}

func TestCatalogRepository_ListNodesFiltersBizTypeAndHeartbeatPreservesFleetMetadata(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertNode(ctx, CloudNode{
		SpaceID: "crypto", NodeID: "collector-0", NodeType: "scf-event",
		Region: "ap-guangzhou", Status: "online",
		Metadata: `{"biz_type":"data_collector","function_name_prefix":"collector","index":0}`,
	}))
	require.NoError(t, repo.UpsertNode(ctx, CloudNode{
		SpaceID: "crypto", NodeID: "factor-0", NodeType: "scf-event",
		Region: "ap-guangzhou", Status: "online",
		Metadata: `{"biz_type":"factor_calculator","function_name_prefix":"collector","index":0}`,
	}))

	require.NoError(t, repo.UpdateHeartbeat(
		ctx, "crypto", "collector-0", "v1", `["collect.binance.kline"]`,
		`{"runtime_code_package_id":"pkg-1"}`,
	))
	nodes, total, err := repo.ListNodes(ctx, "crypto", &pb.GetNodeListReq{
		NodeType: "scf-event", Region: "ap-guangzhou", BizType: "data_collector",
		Page: &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "collector-0", nodes[0].NodeID)
	assert.JSONEq(t,
		`{"biz_type":"data_collector","function_name_prefix":"collector","index":0,"runtime_code_package_id":"pkg-1"}`,
		nodes[0].Metadata,
	)
}

func TestCatalogRepository_ListSCFEventNodesReturnsEveryEligibleNodeWithoutPagination(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	nodes := make([]CloudNode, 0, 1007)
	for i := 0; i < 1002; i++ {
		status := "online"
		switch i {
		case 0:
			status = "unknown"
		case 1:
			status = "new"
		}
		nodes = append(nodes, CloudNode{
			SpaceID:        "crypto",
			NodeID:         fmt.Sprintf("eligible-%04d", i),
			Provider:       "tencent-scf",
			NodeType:       "scf-event",
			CloudAccountID: "acct-1",
			Status:         status,
		})
	}
	nodes = append(nodes,
		CloudNode{SpaceID: "crypto", NodeID: "wrong-provider", Provider: "local", NodeType: "scf-event", Status: "online"},
		CloudNode{SpaceID: "crypto", NodeID: "wrong-type", Provider: "tencent-scf", NodeType: "scf-polling", Status: "online"},
		CloudNode{SpaceID: "crypto", NodeID: "deleted", Provider: "tencent-scf", NodeType: "scf-event", Status: "deleted", IsDeleted: true},
	)
	require.NoError(t, repo.db.WithContext(ctx).CreateInBatches(nodes, 200).Error)

	got, err := repo.ListSCFEventNodes(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1002)
	assert.Equal(t, "eligible-0000", got[0].NodeID)
	assert.Equal(t, "unknown", got[0].Status)
	assert.Equal(t, "eligible-0001", got[1].NodeID)
	assert.Equal(t, "new", got[1].Status)
	assert.Equal(t, "eligible-1001", got[len(got)-1].NodeID)
}

func TestCatalogRepository_HeartbeatDoesNotRegisterUnknownNode(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	require.NoError(t, repo.UpdateHeartbeat(ctx, "crypto", "unknown-function", "v1", `["collect.kline"]`, `{}`))

	node, err := repo.GetNode(ctx, "crypto", "unknown-function")
	require.NoError(t, err)
	assert.Nil(t, node)
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
