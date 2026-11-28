package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func openCloudTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 云账号按账号 ID 新建或整行更新；不存在时读成 nil；删除后读不到。
func TestCloudAccountUpsertGetDelete(t *testing.T) {
	ctx := context.Background()
	accounts := openCloudTestStore(t).CloudAccounts()
	account, err := accounts.Get(ctx, "account-a")
	require.NoError(t, err)
	require.Nil(t, account)

	require.NoError(t, accounts.Upsert(ctx, CloudAccount{AccountID: "account-a", Provider: "tencent", CredentialSecretID: "secret-1", COSRegion: "ap-guangzhou", COSBucket: "bucket-1"}))
	require.NoError(t, accounts.Upsert(ctx, CloudAccount{AccountID: "account-a", Provider: "tencent", CredentialSecretID: "secret-2", AccountName: "主账号"}))
	account, err = accounts.Get(ctx, "account-a")
	require.NoError(t, err)
	require.Equal(t, "secret-2", account.CredentialSecretID)
	require.Equal(t, "主账号", account.AccountName)
	require.Empty(t, account.COSBucket, "更新是整行替换")
	all, err := accounts.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)

	require.NoError(t, accounts.Delete(ctx, "account-a"))
	account, err = accounts.Get(ctx, "account-a")
	require.NoError(t, err)
	require.Nil(t, account)
}

// 最新可用包只看 available，按上传先后取最后一个；按包名全局筛选。
func TestFunctionPackageLatestOnlyCountsAvailablePackagesOfTheSameName(t *testing.T) {
	ctx := context.Background()
	packages := openCloudTestStore(t).FunctionPackages()
	for _, pkg := range []FunctionPackage{
		{PackageID: "p1", PackageName: "collector", CloudAccountID: "account-a", Status: FunctionPackageAvailable},
		{PackageID: "p2", PackageName: "collector", CloudAccountID: "account-a", Status: FunctionPackageAvailable},
		{PackageID: "p3", PackageName: "collector", CloudAccountID: "account-a"},
		{PackageID: "other", PackageName: "other", CloudAccountID: "account-a", Status: FunctionPackageAvailable},
		{PackageID: "p4", PackageName: "collector-alt", CloudAccountID: "account-b", Status: FunctionPackageAvailable},
	} {
		require.NoError(t, packages.Upsert(ctx, pkg))
	}
	latest, err := packages.Latest(ctx, "collector")
	require.NoError(t, err)
	require.Equal(t, "p2", latest.PackageID)
	missing, err := packages.Latest(ctx, "absent")
	require.NoError(t, err)
	require.Nil(t, missing)

	pending, err := packages.Get(ctx, "p3")
	require.NoError(t, err)
	require.Equal(t, FunctionPackagePending, pending.Status)
	pending.Status = FunctionPackageAvailable
	require.NoError(t, packages.Upsert(ctx, *pending))
	latest, err = packages.Latest(ctx, "collector")
	require.NoError(t, err)
	require.Equal(t, "p3", latest.PackageID)

	count, err := packages.CountByAccount(ctx, "account-a")
	require.NoError(t, err)
	require.EqualValues(t, 4, count)
	listed, err := packages.List(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 5)
	require.NoError(t, packages.Delete(ctx, "p3"))
	gone, err := packages.Get(ctx, "p3")
	require.NoError(t, err)
	require.Nil(t, gone)
}
