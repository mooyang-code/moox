package gatewaycontrol

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
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

func controlClient(address string, credentials gatewayauth.Credentials) pb.GatewayControlClientProxy {
	return pb.NewGatewayControlClientProxy(
		client.WithTransport(transport.NewClientTransport()), client.WithTarget("ip://"+address),
		client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(3*time.Second),
		client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithFilter(gatewayauth.NewTRPCClientFilter(credentials, "control", nil)),
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

func signControl(t *testing.T, credentials gatewayauth.Credentials, method, target string, body []byte) http.Header {
	t.Helper()
	headers, err := gatewayauth.Sign(credentials, gatewayauth.Request{Method: "POST", Path: "/" + servicecatalog.GatewayControlPath + "/" + method, TargetNode: target, Callee: servicecatalog.GatewayControlPath, Func: method, Body: body}, time.Now())
	require.NoError(t, err)
	return headers
}

func TestRealTRPCSnapshotHeartbeatAndCallerIsolation(t *testing.T) {
	f := newControlFixture(t)
	address, _ := startControlWire(t, f.service)
	key, err := f.keys.Current(context.Background(), "host-gateway@storage")
	require.NoError(t, err)
	proxy := controlClient(address, key.Credentials())
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
	for _, caller := range []string{"moox-cli", "console", "factor-engine"} {
		key, err := f.keys.Current(context.Background(), caller)
		require.NoError(t, err)
		_, err = controlClient(address, key.Credentials()).PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "storage"})
		require.Error(t, err, caller)
	}
}

func TestRealTRPCVerifiesExactPBAndJSONBytesBeforeDecoding(t *testing.T) {
	f := newControlFixture(t)
	address, _ := startControlWire(t, f.service)
	key, err := f.keys.Current(context.Background(), "host-gateway@storage")
	require.NoError(t, err)
	for _, tc := range []struct {
		serialization int
		body          []byte
	}{
		{codec.SerializationTypePB, []byte{0x0a, 7, 's', 't', 'o', 'r', 'a', 'g', 'e', 0x0a, 7, 's', 't', 'o', 'r', 'a', 'g', 'e'}},
		{codec.SerializationTypeJSON, []byte(" { \"host_id\" : \"storage\" }\n")},
	} {
		headers := signControl(t, key.Credentials(), "PullSnapshot", "control", tc.body)
		raw, err := rawControlCall(address, "PullSnapshot", tc.serialization, tc.body, headers)
		require.NoError(t, err)
		var response pb.PullSnapshotRsp
		require.NoError(t, codec.Unmarshal(tc.serialization, raw, &response))
		require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
		require.True(t, response.Changed)
		// Same decoded protobuf/JSON value, different bytes: the signature fails.
		canonical, err := codec.Marshal(tc.serialization, &pb.PullSnapshotReq{HostId: "storage"})
		require.NoError(t, err)
		headers = signControl(t, key.Credentials(), "PullSnapshot", "control", canonical)
		_, err = rawControlCall(address, "PullSnapshot", tc.serialization, tc.body, headers)
		require.ErrorContains(t, err, "authentication failed")
	}
}

func TestRealTRPCReplaySurvivesControlRestartAndRotation(t *testing.T) {
	f := newControlFixture(t)
	address, svc := startControlWire(t, f.service)
	old, err := f.keys.Current(context.Background(), "host-gateway@storage")
	require.NoError(t, err)
	body, err := codec.Marshal(codec.SerializationTypePB, &pb.PullSnapshotReq{HostId: "storage"})
	require.NoError(t, err)
	headers := signControl(t, old.Credentials(), "PullSnapshot", "control", body)
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, headers)
	require.NoError(t, err)
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, headers)
	require.ErrorContains(t, err, "replayed")
	require.NoError(t, svc.Close(nil))
	require.NoError(t, f.badger.Close())
	f.badger, err = badger.Open(badger.DefaultOptions(f.cachePath).WithLogger(nil))
	require.NoError(t, err)
	f.service = f.newService(t, f.db)
	address, _ = startControlWire(t, f.service)
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, headers)
	require.ErrorContains(t, err, "replayed")
	current, err := f.keys.Rotate(context.Background(), old.Caller)
	require.NoError(t, err)
	for _, key := range []gatewayauth.Credentials{old.Credentials(), current.Credentials()} {
		response, err := controlClient(address, key).PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "storage"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	}
	require.NoError(t, f.keys.Retire(context.Background(), old.Caller, old.KeyID))
	_, err = controlClient(address, old.Credentials()).PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "storage"})
	require.ErrorContains(t, err, "authentication failed")
	_, err = controlClient(address, current.Credentials()).PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "storage"})
	require.NoError(t, err)
}

func TestRealTRPCRejectsWrongTargetMissingHeadersUnknownMethodsAndOversize(t *testing.T) {
	f := newControlFixture(t)
	address, _ := startControlWire(t, f.service)
	key, err := f.keys.Current(context.Background(), "host-gateway@storage")
	require.NoError(t, err)
	body, err := codec.Marshal(codec.SerializationTypePB, &pb.PullSnapshotReq{HostId: "storage"})
	require.NoError(t, err)
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, nil)
	require.Error(t, err)
	headers := signControl(t, key.Credentials(), "PullSnapshot", "storage", body)
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, headers)
	require.Error(t, err)
	headers = signControl(t, key.Credentials(), "Unknown", "control", body)
	_, err = rawControlCall(address, "Unknown", codec.SerializationTypePB, body, headers)
	require.Error(t, err)
	large := []byte(strings.Repeat(" ", maxRequestBytes+1))
	headers = signControl(t, key.Credentials(), "PullSnapshot", "control", large)
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypeJSON, large, headers)
	require.ErrorContains(t, err, "size")
	// Different case spellings survive tRPC's metadata map and must still count
	// as duplicate HTTP authentication headers.
	headers = signControl(t, key.Credentials(), "PullSnapshot", "control", body)
	headers["x-moox-caller"] = []string{key.Caller}
	_, err = rawControlCall(address, "PullSnapshot", codec.SerializationTypePB, body, headers)
	require.Error(t, err)
}

func TestRealTRPCFailsClosedWhenDurableNonceStoreIsUnavailable(t *testing.T) {
	f := newControlFixture(t)
	address, _ := startControlWire(t, f.service)
	key, err := f.keys.Current(context.Background(), "host-gateway@storage")
	require.NoError(t, err)
	require.NoError(t, f.badger.Close())
	_, err = controlClient(address, key.Credentials()).PullSnapshot(context.Background(), &pb.PullSnapshotReq{HostId: "storage"})
	require.ErrorContains(t, err, "replay store unavailable")
}
