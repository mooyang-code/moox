package rpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	pb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestNativeStrategyRPCScopesPBAndJSONAndRejectsConflictingSpace(t *testing.T) {
	service, _, _, _ := newCreateInstanceService(t)
	for _, space := range []string{"space", "other"} {
		require.NoError(t, service.Repo.CreateInstance(t.Context(), store.StrategyInstance{
			InstanceID: "instance-" + space, StrategyID: "create-strategy", SpaceID: space, InputBindingsJSON: []byte(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rpc := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithProtocol("trpc"), server.WithNetwork("tcp"), server.WithTransport(transport.NewServerTransport()))
	pb.RegisterStrategyMgrService(rpc, service)
	done := make(chan error, 1)
	go func() { done <- rpc.Serve() }()
	t.Cleanup(func() {
		require.NoError(t, rpc.Close(nil))
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Strategy native listener did not stop")
		}
	})
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		proxy := pb.NewStrategyMgrClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithSerializationType(serialization), client.WithTimeout(3*time.Second), client.WithTransport(transport.NewClientTransport()), client.WithDisableConnectionPool())
		for _, tc := range []struct {
			name    string
			options []client.Option
			allowed bool
		}{
			{name: "canonical", options: []client.Option{client.WithMetaData("X-Space-Id", []byte("space"))}, allowed: true},
			{name: "lowercase", options: []client.Option{client.WithMetaData("x-space-id", []byte(" space "))}, allowed: true},
			{name: "missing"},
			{name: "conflict", options: []client.Option{client.WithMetaData("X-Space-Id", []byte("space")), client.WithMetaData("x-space-id", []byte("other"))}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rsp, err := proxy.ListStrategyInstances(context.Background(), &pb.ListStrategyInstancesReq{}, tc.options...)
				require.NoError(t, err)
				if tc.allowed {
					require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
					require.Len(t, rsp.GetInstances(), 1)
					require.Equal(t, "space", rsp.GetInstances()[0].GetSpaceId())
				} else {
					require.NotEqual(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
					require.Empty(t, rsp.GetInstances())
				}
			})
		}
	}
}
