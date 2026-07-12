package access

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataValidationHelpers_RejectEmptyIDs(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{}})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode())

	dsRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: "crypto"}})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, dsRsp.GetRetInfo().GetCode())

	datasetRsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{SpaceId: "crypto"}})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, datasetRsp.GetRetInfo().GetCode())
}

func TestGetSpaceAndListSpaces(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	createRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto", Name: "Crypto", Status: "active",
	}})
	mustRetOK(t, createRsp, err)

	getRsp, err := svc.GetSpace(ctx, &pb.GetSpaceReq{SpaceId: "crypto"})
	mustRetOK(t, getRsp, err)
	assert.Equal(t, "Crypto", getRsp.GetSpace().GetName())

	listRsp, err := svc.ListSpaces(ctx, &pb.ListSpacesReq{})
	mustRetOK(t, listRsp, err)
	require.NotEmpty(t, listRsp.GetSpaces())
}
