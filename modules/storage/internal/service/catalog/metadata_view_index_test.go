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
		SpaceId: "space", ViewId: "view", ActiveIndexId: "index", ExpectedActiveRevision: 1,
		ExpectedDesiredRevision: 2, ExpectedActiveSchemaHash: "active", ViewSchemaHash: "desired",
		Columns: []*pb.ViewColumn{{ColumnName: "dataset.column"}},
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
