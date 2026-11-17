package directory

import (
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/listener"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	pb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestLocalDirectoryVersionsPBJSONAndNoVerificationKeys(t *testing.T) {
	state := &snapshot.State{}
	first, err := snapshot.Build("storage", testsnapshot.New(t, "storage", "storage-primary"))
	require.NoError(t, err)
	state.Apply(first)
	opened, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	local := listener.TRPC(opened, Path)
	require.NoError(t, Register(local, state))
	done := make(chan error, 1)
	go func() { done <- local.Serve() }()
	t.Cleanup(func() { _ = local.Close(nil); <-done })
	source, err := gatewayclient.NewLocalDirectorySource(opened.Addr().String(), time.Second)
	require.NoError(t, err)
	defer source.Close()
	update, err := source.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.True(t, update.Changed)
	// 目录就是快照里的主机与部署，版本是快照哈希；调用方按内嵌的服务目录自己展开。
	require.Equal(t, first.Hash(), update.Version)
	require.Len(t, update.Hosts, len(first.Proto().Hosts))
	require.Len(t, update.Deployments, len(first.Proto().Deployments))
	require.Contains(t, update.Hosts, servicecatalog.Host{ID: "storage", Address: "192.0.2.2", Status: servicecatalog.Enabled})
	require.Contains(t, update.Deployments, servicecatalog.Deployment{HostID: "storage", ComponentID: "storage-primary", Status: servicecatalog.Enabled})
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	resolved, unknown := catalog.Resolve(servicecatalog.NewTopology(update.Hosts, update.Deployments))
	require.Empty(t, unknown)
	require.NoError(t, resolved.Validate())
	require.Equal(t, first.Directory().Services, resolved.Services, "调用方展开的目录与网关自己算的一致")
	require.Equal(t, []string{"storage"}, resolved.Services["trpc.moox.storage.PrimaryStore"])
	unchanged, err := source.Fetch(context.Background(), update.Version)
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Equal(t, update.Version, unchanged.Version)
	require.Empty(t, unchanged.Hosts)
	require.Empty(t, unchanged.Deployments)
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		request, err := codec.Marshal(serialization, &pb.GetDirectoryReq{})
		require.NoError(t, err)
		body, err := testrpc.Call(context.Background(), opened.Addr().String(), nil, Path, "GetDirectory", serialization, request, nil)
		require.NoError(t, err)
		response := &pb.GetDirectoryRsp{}
		require.NoError(t, codec.Unmarshal(serialization, body, response))
		require.True(t, response.Changed)
		require.Equal(t, first.Hash(), response.Version)
		require.Len(t, response.Hosts, len(first.Proto().Hosts))
		for _, host := range first.Proto().Hosts {
			require.NotEmpty(t, host.SignPublicKey)
			require.NotContains(t, string(body), string(host.SignPublicKey))
			require.NotContains(t, string(body), base64.StdEncoding.EncodeToString(host.SignPublicKey))
		}
		for _, identity := range first.Proto().Identities {
			require.NotContains(t, string(body), string(identity.SignPublicKey))
			require.NotContains(t, string(body), base64.StdEncoding.EncodeToString(identity.SignPublicKey))
		}
	}
	// A deployment withdrawal changes the directory on the very next request.
	second, err := snapshot.Build("storage", testsnapshot.New(t, "storage"))
	require.NoError(t, err)
	state.Apply(second)
	update, err = source.Fetch(context.Background(), update.Version)
	require.NoError(t, err)
	require.True(t, update.Changed)
	require.Equal(t, second.Hash(), update.Version)
	// 夹具把没放在被测主机上的组件放到 control：storage-primary 不再在 storage 上。
	require.NotContains(t, update.Deployments, servicecatalog.Deployment{HostID: "storage", ComponentID: "storage-primary", Status: servicecatalog.Enabled})
	resolved, _ = catalog.Resolve(servicecatalog.NewTopology(update.Hosts, update.Deployments))
	require.Equal(t, []string{"control"}, resolved.Services["trpc.moox.storage.PrimaryStore"])
	request, err := codec.Marshal(codec.SerializationTypePB, &pb.GetDirectoryReq{CurrentVersion: strings.Repeat("x", 65)})
	require.NoError(t, err)
	_, err = testrpc.Call(context.Background(), opened.Addr().String(), nil, Path, "GetDirectory", codec.SerializationTypePB, request, nil)
	require.Error(t, err)
}
