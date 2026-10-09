package console

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
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
	_, err = d.Forward(context.Background(), "trpc.moox.ops.SysDeploy", "SyncHostPlacements", codec.SerializationTypeJSON, []byte(`{}`))
	require.Equal(t, errs.RetServerAuthFail, errs.Code(err))
}

func TestLocalDispatcherRunsConfiguredFiltersWithRPCNameAndDeadline(t *testing.T) {
	d, err := NewLocalDispatcher()
	require.NoError(t, err)
	implementation := &localAuth{}
	var before, after bool
	interceptor := func(ctx context.Context, req any, next filter.ServerHandleFunc) (any, error) {
		if codec.Message(ctx).ServerRPCName() != "/trpc.moox.infra.Auth/GetLoginSalt" {
			return nil, errors.New("incorrect local RPC name")
		}
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("local RPC deadline missing")
		}
		before = true
		rsp, err := next(ctx, req)
		after = true
		rsp.(*pb.GetLoginSaltRsp).Salt = "filtered-salt"
		return rsp, err
	}
	require.NoError(t, d.Register(&pb.AuthServer_ServiceDesc, implementation, interceptor))
	response, err := d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetLoginSalt", codec.SerializationTypeJSON, []byte(`{"username":"alice"}`))
	require.NoError(t, err)
	require.True(t, before)
	require.True(t, after)
	require.Contains(t, string(response), "filtered-salt")
	require.Equal(t, 1, implementation.calls)
}

func TestLocalDispatcherPropagatesErrorsAndRejectsInvalidInput(t *testing.T) {
	d, err := NewLocalDispatcher()
	require.NoError(t, err)
	implementation := &localAuth{}
	stop := errs.New(100101, "validation rejected")
	require.NoError(t, d.Register(&pb.AuthServer_ServiceDesc, implementation, func(context.Context, any, filter.ServerHandleFunc) (any, error) { return nil, stop }))
	_, err = d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetLoginSalt", codec.SerializationTypeJSON, []byte(`{}`))
	require.ErrorIs(t, err, stop)
	_, err = d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetLoginSalt", codec.SerializationTypeJSON, []byte(`{broken`))
	require.Equal(t, errs.RetServerDecodeFail, errs.Code(err))
	_, err = d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetLoginSalt", codec.SerializationTypePB, nil)
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
	response, err := d.Forward(context.Background(), "trpc.moox.infra.Auth", "GetLoginSalt", codec.SerializationTypeJSON, []byte(`{}`))
	require.Nil(t, response)
	require.Equal(t, errs.RetServerSystemErr, errs.Code(err))
	require.NotContains(t, err.Error(), "private implementation detail")
}
