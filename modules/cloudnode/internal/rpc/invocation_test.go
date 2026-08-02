package rpc

import (
	"context"
	"testing"
	"time"

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

func TestInvokeSync_ValidatesInput(t *testing.T) {
	svc := &Service{catalog: newCatalogForAccountTests(t)}

	rsp, err := svc.InvokeSync(context.Background(), &pb.InvokeSyncReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.InvokeSync(context.Background(), &pb.InvokeSyncReq{SpaceId: "crypto"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.InvokeSync(context.Background(), &pb.InvokeSyncReq{
		SpaceId:  "crypto",
		Payloads: []*pb.InvokeSyncPayload{{RequestId: "r1", Payload: `{}`}},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetInvocationId())
}

func TestInvocationHelpers_ShouldFormatResults(t *testing.T) {
	success, failed, timeout := countSyncResults([]*pb.InvokeSyncResult{
		{Status: pb.InvocationStatus_INVOCATION_STATUS_SUCCESS},
		{Status: pb.InvocationStatus_INVOCATION_STATUS_FAILED, ErrorMessage: "timeout: deadline"},
		{Status: pb.InvocationStatus_INVOCATION_STATUS_FAILED, ErrorMessage: "boom"},
	})
	assert.Equal(t, int32(1), success)
	assert.Equal(t, int32(1), failed)
	assert.Equal(t, int32(1), timeout)
	assert.True(t, isTimeoutSyncResult(&pb.InvokeSyncResult{ErrorMessage: "timeout: x"}))

	id, err := newInvocationID(time.Unix(0, 0).UTC())
	require.NoError(t, err)
	assert.Contains(t, id, "inv_")

	assert.Equal(t, "Event", scfInvokeTypeToString(pb.ScfInvokeType_SCF_INVOKE_TYPE_EVENT))
	assert.Equal(t, "RequestResponse", scfInvokeTypeToString(pb.ScfInvokeType_SCF_INVOKE_TYPE_UNSPECIFIED))
	assert.Equal(t, "success", invocationStatusText(pb.InvocationStatus_INVOCATION_STATUS_SUCCESS))
	assert.Equal(t, "pending", invocationStatusText(pb.InvocationStatus_INVOCATION_STATUS_UNSPECIFIED))

	st := returnResultStruct(`{"k":"v"}`)
	assert.Equal(t, "v", st.GetFields()["k"].GetStringValue())
	raw := returnResultStruct("not-json")
	assert.Equal(t, "not-json", raw.GetFields()["raw"].GetStringValue())
}

func TestInvokeFunction_WithEventData(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	require.NoError(t, catalog.UpsertAccount(context.Background(), store.CloudAccount{
		AccountID: "acct-1", Provider: "tencent", CredentialSecretID: "secret-1",
	}))
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "node-1", CloudAccountID: "acct-1", Region: "ap-guangzhou",
		Status: "online",
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
