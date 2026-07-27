package rpc

import (
	"context"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestPackageHelpers_ShouldSanitizeAndConvert(t *testing.T) {
	assert.Equal(t, "pkg_v1_upload-1", buildPackageID("pkg", "v1", "upload-1"))
	assert.Equal(
		t,
		"moox/cloud-packages/collector/pkg/v1/pkg_v1_upload-1/a.zip",
		buildPackageCOSPath("collector", "pkg", "v1", "pkg_v1_upload-1", "a.zip"),
	)
	assert.Equal(t, "package.zip", sanitizePackageFileName("@@@"))
	assert.Equal(t, "ok-name", sanitizePackagePathSegment(" ok-name "))
	assert.Equal(t, "a_b", sanitizePackagePathSegment("a/b"))

	assert.Equal(t, "collector", packageTypeToDB(pb.PackageType_PACKAGE_TYPE_COLLECTOR))
	assert.Equal(t, "factor", packageTypeToDB(pb.PackageType_PACKAGE_TYPE_FACTOR))
	assert.Equal(t, "custom", packageTypeToDB(pb.PackageType_PACKAGE_TYPE_CUSTOM))
	assert.Equal(t, pb.PackageType_PACKAGE_TYPE_COLLECTOR, packageTypeToPB("collector"))
	assert.Equal(t, pb.PackageType_PACKAGE_TYPE_UNSPECIFIED, packageTypeToPB("x"))

	assert.Equal(t, pb.PackageStatus_PACKAGE_STATUS_PENDING, packageStatusToPB("pending"))
	assert.Equal(t, pb.PackageStatus_PACKAGE_STATUS_AVAILABLE, packageStatusToPB("available"))
	assert.Equal(t, pb.PackageStatus_PACKAGE_STATUS_FAILED, packageStatusToPB("failed"))
	assert.Equal(t, pb.PackageStatus_PACKAGE_STATUS_DELETED, packageStatusToPB("deleted"))
}

func TestPackagePBConverters(t *testing.T) {
	pkg := store.FunctionPackage{
		ID: 1, SpaceID: "crypto", PackageID: "pkg-1", PackageName: "collector",
		Version: "v1", Runtime: "CustomRuntime", PackageType: "collector",
		WorkloadType: "collect.kline", FileSize: 10, FileMD5: "md5",
		CloudAccountID: "acct-1", COSRegion: "ap-guangzhou", COSBucket: "bucket",
		COSPath: "/obj.zip", Status: "available", CreateTime: time.Unix(1, 0).UTC(),
	}
	item := toPBPackageListItem(pkg)
	assert.Equal(t, "pkg-1", item.GetPackageId())
	assert.Equal(t, pb.PackageType_PACKAGE_TYPE_COLLECTOR, item.GetPackageType())

	detail := toPBPackageDetail(pkg)
	assert.Equal(t, "pkg-1", detail.GetPackageId())
	assert.Contains(t, detail.GetCosUrl(), "bucket.cos.ap-guangzhou")
	assert.Equal(t, "", cosURL(store.FunctionPackage{}))
}

func TestPackageRPC_DetailAndDelete(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto")

	require.NoError(t, catalog.UpsertPackage(context.Background(), store.FunctionPackage{
		SpaceID: "crypto", PackageID: "pkg-1", PackageName: "collector",
		Version: "v1", Runtime: "CustomRuntime", PackageType: "collector", Status: "available",
	}))

	detailRsp, err := svc.GetPackageDetail(ctx, &pb.GetPackageDetailReq{PackageId: "pkg-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, detailRsp.GetRetInfo().GetCode())
	assert.Equal(t, "pkg-1", detailRsp.GetDetail().GetPackageId())

	missing, err := svc.GetPackageDetail(ctx, &pb.GetPackageDetailReq{PackageId: "missing"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())

	delRsp, err := svc.DeletePackage(ctx, &pb.DeletePackageReq{PackageId: "pkg-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, delRsp.GetRetInfo().GetCode())
}
