package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestInvokeFunction_ValidatesInput(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	svc := &Service{catalog: catalog}
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto")

	rsp, err := svc.InvokeFunction(ctx, &pb.InvokeFunctionReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.InvokeFunction(context.Background(), &pb.InvokeFunctionReq{NodeId: "node-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.InvokeFunction(ctx, &pb.InvokeFunctionReq{NodeId: "missing"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

func TestInvokeFunction_WithEventData(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	require.NoError(t, catalog.UpsertAccount(context.Background(), store.CloudAccount{
		AccountID: "acct-1", Provider: "tencent", CredentialSecretID: "secret-1",
	}))
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "node-1", CloudAccountID: "acct-1", Region: "ap-guangzhou",
	}))
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "sid", SecretKey: "skey"}},
	}
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto")
	event, err := structpb.NewStruct(map[string]any{"k": "v"})
	require.NoError(t, err)

	rsp, err := svc.InvokeFunction(ctx, &pb.InvokeFunctionReq{
		NodeId: "node-1", EventData: event,
	})
	require.NoError(t, err)
	// Real SCF call is skipped; inner error is expected without network.
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestInvocationHelpers_ShouldFormatResults(t *testing.T) {
	assert.Equal(t, "Event", scfInvokeTypeToString(pb.ScfInvokeType_SCF_INVOKE_TYPE_EVENT))
	assert.Equal(t, "RequestResponse", scfInvokeTypeToString(pb.ScfInvokeType_SCF_INVOKE_TYPE_UNSPECIFIED))

	st := returnResultStruct(`{"k":"v"}`)
	assert.Equal(t, "v", st.GetFields()["k"].GetStringValue())
	raw := returnResultStruct("not-json")
	assert.Equal(t, "not-json", raw.GetFields()["raw"].GetStringValue())
}
