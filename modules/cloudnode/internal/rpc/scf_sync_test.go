package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/require"
)

func TestPreviewAndImportSCFFunctionsAreSpaceScopedAndIdempotent(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	require.NoError(t, catalog.UpsertAccount(context.Background(), store.CloudAccount{AccountID: "account-a", Provider: "tencent", CredentialSecretID: "secret"}))
	fake := &fakeSCFClient{
		inventory:  []tencentscf.DiscoveryFunction{{FunctionRef: tencentscf.FunctionRef{Region: "ap-guangzhou", Namespace: "default", FunctionName: "moox-fetcher-crypto-market-0"}, Status: "Active", Runtime: "Go1", Type: "Event"}},
		getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{Status: "Active", Runtime: "Go1", Type: "Event", Environment: map[string]string{"MOOX_SPACE_ID": "crypto_market", "MOOX_CODE_PACKAGE_ID": "pkg-1"}}}},
	}
	svc := &Service{catalog: catalog, credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}}, scfClientFactory: func(cloudcredential.TencentCredential) scfProvisioner { return fake }}
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto_market")
	preview, err := svc.PreviewSCFFunctions(ctx, &pb.PreviewSCFFunctionsReq{AccountId: "account-a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, preview.GetRetInfo().GetCode())
	require.Len(t, preview.GetFunctions(), 18)
	// The fake inventory is returned for every supported region, so duplicate
	// identities are blocked rather than silently overwriting one node.
	for _, candidate := range preview.GetFunctions() {
		require.False(t, candidate.GetImportable())
		require.Contains(t, candidate.GetReason(), "same function name")
	}

	fake.inventoryRegion = "ap-guangzhou"
	fake.getResults = []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{Status: "Active", Runtime: "Go1", Type: "Event", Environment: map[string]string{"MOOX_SPACE_ID": "crypto_market", "MOOX_CODE_PACKAGE_ID": "pkg-1"}}}}
	preview, err = svc.PreviewSCFFunctions(ctx, &pb.PreviewSCFFunctionsReq{AccountId: "account-a"})
	require.NoError(t, err)
	require.Len(t, preview.GetFunctions(), 1)
	require.True(t, preview.GetFunctions()[0].GetImportable())
	fake.getResults = []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{Status: "Active", Runtime: "Go1", Type: "Event", Environment: map[string]string{"MOOX_SPACE_ID": "crypto_market", "MOOX_CODE_PACKAGE_ID": "pkg-1"}}}}

	importRsp, err := svc.ImportSCFFunctions(ctx, &pb.ImportSCFFunctionsReq{AccountId: "account-a", Functions: []*pb.SCFFunctionRef{{Region: "ap-guangzhou", Namespace: "default", FunctionName: "moox-fetcher-crypto-market-0"}}})
	require.NoError(t, err)
	require.Equal(t, int32(1), importRsp.GetCreated())
	node, err := catalog.GetNode(ctx, "crypto_market", "moox-fetcher-crypto-market-0")
	require.NoError(t, err)
	require.Equal(t, "pkg-1", node.PackageID)
	require.NotContains(t, node.Metadata, "MOOX_SECRET")
}
