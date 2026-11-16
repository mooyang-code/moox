package gatewaycontrol

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func startControlWire(t *testing.T, implementation *Service) (string, server.Service) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := server.New(server.WithServiceName(servicecatalog.GatewayControlPath), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithListener(listener), server.WithAddress(listener.Addr().String()), ServerOption())
	require.NoError(t, Register(svc, implementation))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	return listener.Addr().String(), svc
}

// controlClient 模拟控制机网关转发过来的调用：带上它验过的签名主机。
func controlClient(address, verifiedHost string) pb.GatewayControlClientProxy {
	return pb.NewGatewayControlClientProxy(
		client.WithTransport(transport.NewClientTransport()), client.WithTarget("ip://"+address),
		client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(3*time.Second),
		client.WithMetaData(gatewayauth.HeaderVerifiedHost, []byte(verifiedHost)),
	)
}

func rawControlCall(address, method string, serialization int, body []byte, headers http.Header) ([]byte, error) {
	ctx, msg := codec.WithCloneMessage(trpc.BackgroundContext())
	defer codec.PutBackMessage(msg)
	msg.WithClientRPCName("/" + servicecatalog.GatewayControlPath + "/" + method)
	options := []client.Option{
		client.WithTransport(transport.NewClientTransport()), client.WithTarget("ip://" + address), client.WithNetwork("tcp"),
		client.WithProtocol("trpc"), client.WithServiceName(servicecatalog.GatewayControlPath), client.WithCalleeMethod(method),
		client.WithTimeout(3 * time.Second), client.WithSerializationType(serialization), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
	}
	for key, values := range headers {
		for _, value := range values {
			options = append(options, client.WithMetaData(key, []byte(value)))
		}
	}
	response := &codec.Body{}
	err := client.New().Invoke(ctx, &codec.Body{Data: body}, response, options...)
	return response.Data, err
}

func verifiedHeader(host string) http.Header {
	return http.Header{gatewayauth.HeaderVerifiedHost: {host}}
}

// 网关验过签之后转进来的调用：按签名主机头认主机，一台主机只能取自己的快照、报自己的心跳。
func TestRealTRPCSnapshotHeartbeatAndHostIsolation(t *testing.T) {
	f := newControlFixture(t)
	address, _ := startControlWire(t, f.service)
	proxy := controlClient(address, "storage")
	response, err := proxy.PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "storage"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	require.True(t, response.Changed)
	require.NotEmpty(t, response.Snapshot.VerificationKeys)
	unchanged, err := proxy.PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "storage", CurrentHash: response.Snapshot.Hash})
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Nil(t, unchanged.Snapshot)
	report, err := proxy.ReportStatus(context.Background(), &pb.ReportStatusReq{HostId: "storage", InstanceId: "wire-instance", Version: "v-test", AppliedHash: response.Snapshot.Hash, RouteCount: int32(len(response.Snapshot.Routes))})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, report.GetRetInfo().GetCode())
	status := f.status(t)
	require.Equal(t, "wire-instance", status.InstanceID)
	require.Equal(t, response.Snapshot.Hash, status.AppliedHash)
	require.Equal(t, response.Snapshot.Hash, status.ExpectedHash)
	_, err = proxy.PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "control"})
	require.Error(t, err)
	_, err = proxy.ReportStatus(context.Background(), &pb.ReportStatusReq{HostId: "control", InstanceId: "forged", Version: "v-test"})
	require.Error(t, err)
}

func TestRealTRPCAcceptsPBAndJSON(t *testing.T) {
	f := newControlFixture(t)
	address, _ := startControlWire(t, f.service)
	for _, tc := range []struct {
		serialization int
		body          []byte
	}{
		{codec.SerializationTypePB, []byte{0x0a, 7, 's', 't', 'o', 'r', 'a', 'g', 'e'}},
		{codec.SerializationTypeJSON, []byte(" { \"host_id\" : \"storage\" }\n")},
	} {
		raw, err := rawControlCall(address, "PullSnapshot", tc.serialization, tc.body, verifiedHeader("storage"))
		require.NoError(t, err)
		var response pb.PullSnapshotRsp
		require.NoError(t, codec.Unmarshal(tc.serialization, raw, &response))
		require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
		require.True(t, response.Changed)
	}
}

// 没有签名主机头（没经过网关）、头不是合法主机、头重复、未知方法、超大请求都拒绝。非主机身份经网关进来时头是空的，同样拒绝。
func TestRealTRPCRejectsMissingOrInvalidVerifiedHostUnknownMethodsAndOversize(t *testing.T) {
	f := newControlFixture(t)
	address, _ := startControlWire(t, f.service)
	body, err := codec.Marshal(codec.SerializationTypePB, &pb.PullSnapshotReq{HostId: "storage"})
	require.NoError(t, err)
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, nil)
	require.ErrorContains(t, err, "verified signing host")
	for _, host := range []string{"", "Storage", "host-gateway@storage", "../storage"} {
		_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, verifiedHeader(host))
		require.ErrorContains(t, err, "verified signing host", host)
	}
	// 大小写不同的同名头在 tRPC 的元数据表里并存，按重复处理。
	headers := verifiedHeader("storage")
	headers["x-moox-verified-host"] = []string{"control"}
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, headers)
	require.ErrorContains(t, err, "verified signing host")
	_, err = rawControlCall(address, "Unknown", codec.SerializationTypePB, body, verifiedHeader("storage"))
	require.Error(t, err)
	large := []byte(strings.Repeat(" ", maxRequestBytes+1))
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypeJSON, large, verifiedHeader("storage"))
	require.ErrorContains(t, err, "size")
}
