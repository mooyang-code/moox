package console

import (
	"context"
	"errors"
	"testing"

	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

type machineSecret struct {
	pb.UnimplementedSecretMgr
	calls int
}

func (s *machineSecret) GetSecretValue(context.Context, *pb.GetSecretValueReq) (*pb.GetSecretValueRsp, error) {
	s.calls++
	return &pb.GetSecretValueRsp{}, nil
}

func TestLocalDispatcherRejectsMachineMethodBeforeDecodingOrExecuting(t *testing.T) {
	d, err := NewLocalDispatcher()
	require.NoError(t, err)
	implementation := &machineSecret{}
	require.NoError(t, d.Register(&pb.SecretMgrServer_ServiceDesc, implementation))
	_, err = d.Forward(context.Background(), "trpc.moox.ops.SecretMgr", "GetSecretValue", codec.SerializationTypeJSON, []byte(`invalid JSON`))
	require.Equal(t, errs.RetServerAuthFail, errs.Code(err))
	require.Zero(t, implementation.calls)
	_, err = d.Forward(context.Background(), "trpc.moox.ops.SysDeploy", "SyncTopology", codec.SerializationTypeJSON, []byte(`{}`))
	require.Equal(t, errs.RetServerAuthFail, errs.Code(err))
}

func TestLocalDispatcherRunsConfiguredFiltersWithRPCNameAndDeadline(t *testing.T) {
	d, err := NewLocalDispatcher()
	require.NoError(t, err)
	implementation := &localAuth{}
	var before, after bool
	interceptor := func(ctx context.Context, req any, next filter.ServerHandleFunc) (any, error) {
		if codec.Message(ctx).ServerRPCName() != "/trpc.moox.infra.Auth/GetUserInfo" {
			return nil, errors.New("incorrect local RPC name")
		}
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("local RPC deadline missing")
		}
		before = true
		rsp, err := next(ctx, req)
		after = true
		rsp.(*pb.GetUserInfoRsp).UserInfo = &pb.UserInfo{Username: "filtered-user"}
		return rsp, err
	}
	require.NoError(t, d.Register(&pb.AuthServer_ServiceDesc, implementation, interceptor))
	implementation.sid = "sid-filter"
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, authmodel.CtxUserID, []byte("u1"))
	trpc.SetMetaData(ctx, authmodel.CtxSessionID, []byte("sid-filter"))
	ctx = gatewayclient.WithCallMetadata(ctx, gatewayclient.CallMetadata{UserID: "u1"})
	response, err := d.Forward(ctx, "trpc.moox.infra.Auth", "GetUserInfo", codec.SerializationTypeJSON, []byte(`{}`))
	require.NoError(t, err)
	require.True(t, before)
	require.True(t, after)
	require.Contains(t, string(response), "filtered-user")
	require.Equal(t, 1, implementation.calls)
}

func TestLocalDispatcherPropagatesErrorsAndRejectsInvalidInput(t *testing.T) {
	d, err := NewLocalDispatcher()
	require.NoError(t, err)
	implementation := &localAuth{}
	stop := errs.New(100101, "validation rejected")
	require.NoError(t, d.Register(&pb.AuthServer_ServiceDesc, implementation, func(context.Context, any, filter.ServerHandleFunc) (any, error) { return nil, stop }))
	_, err = d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetUserInfo", codec.SerializationTypeJSON, []byte(`{}`))
	require.ErrorIs(t, err, stop)
	_, err = d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetUserInfo", codec.SerializationTypeJSON, []byte(`{broken`))
	require.Equal(t, errs.RetServerDecodeFail, errs.Code(err))
	_, err = d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetUserInfo", codec.SerializationTypePB, nil)
	require.Equal(t, errs.RetServerDecodeFail, errs.Code(err))
	require.Zero(t, implementation.calls)
	require.Error(t, d.Register(&pb.AuthServer_ServiceDesc, implementation), "duplicate bindings rejected")
	require.Error(t, d.Register(&server.ServiceDesc{ServiceName: "trpc.moox.storage.Metadata"}, struct{}{}))
}

func TestLocalDispatcherContainsPanic(t *testing.T) {
	d, err := NewLocalDispatcher()
	require.NoError(t, err)
	require.NoError(t, d.Register(&pb.AuthServer_ServiceDesc, &localAuth{}, func(context.Context, any, filter.ServerHandleFunc) (any, error) {
		panic("private implementation detail")
	}))
	response, err := d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetUserInfo", codec.SerializationTypeJSON, []byte(`{}`))
	require.Nil(t, response)
	require.Equal(t, errs.RetServerSystemErr, errs.Code(err))
	require.NotContains(t, err.Error(), "private implementation detail")
}
