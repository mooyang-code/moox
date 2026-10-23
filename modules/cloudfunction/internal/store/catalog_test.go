package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	cloudfunctionschema "github.com/mooyang-code/moox/modules/cloudfunction/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestCatalog(t *testing.T) *CatalogRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(cloudfunctionschema.AllSQL()).Error)
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

func TestCatalogRepository_InstanceLifecycle(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()

	require.NoError(t, repo.UpsertInstance(ctx, FunctionInstance{
		SpaceID: "crypto", InstanceID: "instance-a", CloudAccountID: "acct-1",
		FunctionType: "scf-event", Region: "ap-guangzhou", Namespace: "default",
		DeploymentID: "dep-1",
	}))
	instance, err := repo.GetInstance(ctx, "crypto", "instance-a")
	require.NoError(t, err)
	require.NotNil(t, instance)
	assert.Equal(t, "tencent-scf", instance.Provider)

	instances, total, err := repo.ListInstances(ctx, "crypto", &pb.ListFunctionInstancesReq{
		InstanceId: "instance", Region: "ap-guangzhou",
		Keyword: "instance", Page: &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Len(t, instances, 1)

	require.NoError(t, repo.UpdateInstanceDeployment(ctx, "crypto", "instance-a", "pkg-1", "v2", nil, map[string]string{"MOOX_FETCH_MAX_INFLIGHT_REQUESTS": "7"}, true))
	updated, err := repo.GetInstance(ctx, "crypto", "instance-a")
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "scf-event", updated.FunctionType)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(updated.Metadata), &metadata))
	assert.Equal(t, true, metadata["collector_publish_fenced"])
	require.NoError(t, repo.DeleteInstances(ctx, "crypto", []string{"instance-a"}))
	deleted, err := repo.GetInstance(ctx, "crypto", "instance-a")
	require.NoError(t, err)
	assert.Nil(t, deleted)
}

func TestCatalogRepository_ListInstancesFiltersWorkloadType(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertInstance(ctx, FunctionInstance{
		SpaceID: "crypto", InstanceID: "collector-0", FunctionType: "scf-event",
		Region:   "ap-guangzhou",
		Metadata: `{"workload_type":"data_collector","function_name_prefix":"collector","index":0}`,
	}))
	require.NoError(t, repo.UpsertInstance(ctx, FunctionInstance{
		SpaceID: "crypto", InstanceID: "factor-0", FunctionType: "scf-event",
		Region:   "ap-guangzhou",
		Metadata: `{"workload_type":"factor_calculator","function_name_prefix":"collector","index":0}`,
	}))

	instances, total, err := repo.ListInstances(ctx, "crypto", &pb.ListFunctionInstancesReq{
		FunctionType: "scf-event", Region: "ap-guangzhou", WorkloadType: "data_collector",
		Page: &pb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, instances, 1)
	assert.Equal(t, "collector-0", instances[0].InstanceID)
	assert.JSONEq(t, `{"workload_type":"data_collector","function_name_prefix":"collector","index":0}`, instances[0].Metadata)
}

func TestPageFromCommon_NormalizesBounds(t *testing.T) {
	page, size := pageFromCommon(nil)
	assert.Equal(t, 1, page)
	assert.Equal(t, 50, size)
	page, size = pageFromCommon(&pb.Page{Page: 0, Size: 5000})
	assert.Equal(t, 1, page)
	assert.Equal(t, 1000, size)
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

func TestCatalogRepository_AccountRecreateAfterDeleteCanBeDeletedAgain(t *testing.T) {
	repo := newTestCatalog(t)
	ctx := context.Background()
	account := CloudAccount{AccountID: "acct-1", AccountName: "old", Provider: "tencent", CredentialSecretID: "secret-1"}

	require.NoError(t, repo.UpsertAccount(ctx, account))
	require.NoError(t, repo.DeleteAccount(ctx, "acct-1"))
	account.AccountName = "new"
	account.CredentialSecretID = "secret-2"
	require.NoError(t, repo.UpsertAccount(ctx, account)) // 同名重建复用旧行

	got, err := repo.GetAccount(ctx, "acct-1")
	require.NoError(t, err)
	require.NotNil(t, got, "重建后账户应恢复为有效")
	assert.Equal(t, "new", got.AccountName)
	assert.Equal(t, "secret-2", got.CredentialSecretID)

	require.NoError(t, repo.DeleteAccount(ctx, "acct-1"), "再次删除不能违反唯一约束")
	missing, err := repo.GetAccount(ctx, "acct-1")
	require.NoError(t, err)
	assert.Nil(t, missing)
	_, total, err := repo.ListAccounts(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
}
