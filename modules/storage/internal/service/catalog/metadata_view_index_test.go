package catalog

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestCommitViewSchemaExtensionRequiresViewRoleHMAC(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.InitSchema(ctx))
	service, err := NewMetadataService(store, nil, Options{AuthSecret: "node-secret", ViewAuthSecret: "view-secret"})
	require.NoError(t, err)

	req := &pb.CommitViewSchemaExtensionReq{
		SpaceId: "space", ViewId: "view", ActiveIndexId: "index", ExpectedViewRevision: 1,
		ExpectedSchemaHash: "active", ViewSchemaHash: "desired",
		Columns: []*pb.ViewColumn{{ColumnName: "column"}},
	}
	rejected, err := service.CommitViewSchemaExtension(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, rejected.GetRetInfo().GetCode())

	req.AuthInfo = &pb.AuthInfo{AppId: "storage-primary", AppKey: serviceAuthKey("view-secret", "storage-primary")}
	rejected, err = service.CommitViewSchemaExtension(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, rejected.GetRetInfo().GetCode())
	req.AuthInfo = &pb.AuthInfo{AppId: "storage-view", AppKey: serviceAuthKey("wrong-secret", "storage-view")}
	rejected, err = service.CommitViewSchemaExtension(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, rejected.GetRetInfo().GetCode())

	req.AuthInfo = &pb.AuthInfo{AppId: "storage-view", AppKey: serviceAuthKey("view-secret", "storage-view")}
	rejected, err = service.CommitViewSchemaExtension(ctx, req)
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, rejected.GetRetInfo().GetCode())
}

// storage-view 专用的 Metadata 方法都按 storage-view 的服务签名验证：没有签名、签名错误都被拒绝，签名正确才进入处理。
func TestViewOnlyMetadataMethodsRequireViewServiceSignature(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.InitSchema(ctx))
	service, err := NewMetadataService(store, nil, Options{AuthSecret: "node-secret", ViewAuthSecret: "view-secret"})
	require.NoError(t, err)

	calls := map[string]func(*pb.AuthInfo) *pb.RetInfo{
		"CompleteViewInitialBuild": func(a *pb.AuthInfo) *pb.RetInfo {
			rsp, _ := service.CompleteViewInitialBuild(ctx, &pb.CompleteViewInitialBuildReq{AuthInfo: a})
			return rsp.GetRetInfo()
		},
		"UpsertViewPeriodDatasetState": func(a *pb.AuthInfo) *pb.RetInfo {
			rsp, _ := service.UpsertViewPeriodDatasetState(ctx, &pb.UpsertViewPeriodDatasetStateReq{AuthInfo: a})
			return rsp.GetRetInfo()
		},
		"ListViewPeriodDatasetStates": func(a *pb.AuthInfo) *pb.RetInfo {
			rsp, _ := service.ListViewPeriodDatasetStates(ctx, &pb.ListViewPeriodDatasetStatesReq{AuthInfo: a})
			return rsp.GetRetInfo()
		},
		"RecordViewSyncPoint": func(a *pb.AuthInfo) *pb.RetInfo {
			rsp, _ := service.RecordViewSyncPoint(ctx, &pb.RecordViewSyncPointReq{AuthInfo: a})
			return rsp.GetRetInfo()
		},
	}
	for name, call := range calls {
		require.Equal(t, pb.ErrorCode_NO_PERMISSION, call(nil).GetCode(), name+" without auth")
		require.Equal(t, pb.ErrorCode_NO_PERMISSION, call(&pb.AuthInfo{AppId: "storage-view", AppKey: serviceAuthKey("wrong-secret", "storage-view")}).GetCode(), name+" wrong signature")
		require.NotEqual(t, pb.ErrorCode_NO_PERMISSION, call(&pb.AuthInfo{AppId: "storage-view", AppKey: serviceAuthKey("view-secret", "storage-view")}).GetCode(), name+" correct signature")
	}
}
