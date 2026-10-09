package directory

import (
	"context"
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
	require.NoError(t, update.Directory.Validate())
	unchanged, err := source.Fetch(context.Background(), update.Directory.Version)
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Equal(t, update.Directory.Version, unchanged.Directory.Version)
	require.Empty(t, unchanged.Directory.Hosts)
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		request, err := codec.Marshal(serialization, &pb.GetDirectoryReq{})
		require.NoError(t, err)
		body, err := testrpc.Call(context.Background(), opened.Addr().String(), nil, Path, "GetDirectory", serialization, request, nil)
		require.NoError(t, err)
		response := &pb.GetDirectoryRsp{}
		require.NoError(t, codec.Unmarshal(serialization, body, response))
		require.True(t, response.Changed)
		for _, key := range first.Proto().VerificationKeys {
			require.NotContains(t, string(body), key.KeyId)
			require.NotContains(t, string(body), string(key.Secret))
		}
	}
	// A placement withdrawal changes the directory on the very next request.
	second, err := snapshot.Build("storage", testsnapshot.New(t, "storage"))
	require.NoError(t, err)
	state.Apply(second)
	update, err = source.Fetch(context.Background(), update.Directory.Version)
	require.NoError(t, err)
	require.True(t, update.Changed)
	require.NotContains(t, update.Directory.Services, "trpc.moox.storage.PrimaryStore")
	request, err := codec.Marshal(codec.SerializationTypePB, &pb.GetDirectoryReq{CurrentVersion: strings.Repeat("x", 65)})
	require.NoError(t, err)
	_, err = testrpc.Call(context.Background(), opened.Addr().String(), nil, Path, "GetDirectory", codec.SerializationTypePB, request, nil)
	require.Error(t, err)
}
