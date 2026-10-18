package rpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

// Use the production handler without HTTP headers or a space injection filter.
func TestNativeGatewayMetadataScopesProductionNodeList(t *testing.T) {
	catalog := newCatalogForAccountTests(t)
	for _, space := range []string{"crypto", "stock"} {
		require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
			SpaceID: space, NodeID: space + "-node", NodeType: "scf-event", Region: "ap-singapore",
		}))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener),
		server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	pb.RegisterCloudNodeMgrService(service, &Service{catalog: catalog})
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { require.NoError(t, service.Close(nil)) })
	proxy := pb.NewCloudNodeMgrClientProxy(client.WithTarget("ip://"+listener.Addr().String()),
		client.WithProtocol("trpc"), client.WithNetwork("tcp"), client.WithTransport(transport.NewClientTransport()),
		client.WithTimeout(5*time.Second))
	for _, space := range []string{"crypto", "stock", ""} {
		t.Run(space, func(t *testing.T) {
			response, callErr := proxy.GetNodeList(t.Context(), &pb.GetNodeListReq{},
				client.WithMetaData(spacecontext.SpaceIDHeader, []byte(space)))
			require.NoError(t, callErr)
			if space == "" {
				require.Equal(t, pb.ErrorCode_INVALID_PARAM, response.GetRetInfo().GetCode())
				require.Empty(t, response.GetItems())
				return
			}
			require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
			require.Len(t, response.GetItems(), 1)
			require.Equal(t, space+"-node", response.GetItems()[0].GetNodeId())
		})
	}
}
