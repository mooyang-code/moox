package listener

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/testcert"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	_ "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const echoService = "trpc.moox.test.Echo"

func fixture(t *testing.T) (hostgatewayconfig.Config, *tlsconfig.Material) {
	t.Helper()
	paths := testcert.Files(t, testcert.New(t, nil), "storage", nil)
	cfg := hostgatewayconfig.Default("storage", "control", "192.0.2.1", "fixture-key")
	cfg.TLS, cfg.Store.Path = paths, filepath.Join(t.TempDir(), "cache")
	reserved := make([]net.Listener, 3)
	for i := range reserved {
		opened, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		reserved[i] = opened
		t.Cleanup(func() { _ = opened.Close() })
	}
	cfg.Server.RemoteAddr = reserved[0].Addr().String()
	cfg.Server.LocalAddr = reserved[1].Addr().String()
	cfg.Server.HealthAddr = reserved[2].Addr().String()
	for _, opened := range reserved {
		require.NoError(t, opened.Close())
	}
	material, err := tlsconfig.Load(cfg.Host.ID, cfg.TLS)
	require.NoError(t, err)
	return cfg, material
}

func echo(t *testing.T, opened net.Listener) {
	t.Helper()
	svc := TRPC(opened, echoService)
	desc := &server.ServiceDesc{
		ServiceName: echoService, HandlerType: ((*interface{})(nil)),
		Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
			request := &codec.Body{}
			filters, err := f(request)
			if err != nil {
				return nil, err
			}
			return filters.Filter(ctx, request, func(_ context.Context, body interface{}) (interface{}, error) {
				return &codec.Body{Data: append([]byte(nil), body.(*codec.Body).Data...)}, nil
			})
		}}},
	}
	require.NoError(t, svc.Register(desc, struct{}{}))
	done := make(chan error, 1)
	go func() { done <- svc.Serve() }()
	t.Cleanup(func() {
		require.NoError(t, svc.Close(nil))
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("tRPC server did not stop after Close")
		}
	})
}

func roundTrip(address, caFile, serverName string, serialization int, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx, message := codec.WithNewMessage(ctx)
	defer codec.PutBackMessage(message)
	message.WithClientRPCName("/" + echoService + "/Echo")
	options := []client.Option{
		client.WithTarget("ip://" + address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(echoService), client.WithCalleeMethod("Echo"),
		client.WithSerializationType(serialization), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithTransport(transport.NewClientTransport()), client.WithMultiplexed(false),
		client.WithPool(connpool.NewConnectionPool(connpool.WithMaxIdle(0))),
		client.WithTimeout(2 * time.Second), client.WithDialTimeout(time.Second),
	}
	if caFile != "" {
		options = append(options, client.WithTLS("", "", caFile, serverName))
	}
	response := &codec.Body{}
	if err := client.DefaultClient.Invoke(ctx, &codec.Body{Data: body}, response, options...); err != nil {
		return nil, err
	}
	return response.Data, nil
}

func TestRemoteTLSAndLocalTRPCPreserveRawBodies(t *testing.T) {
	cfg, material := fixture(t)
	opened, err := Open(context.Background(), cfg, material)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })
	echo(t, opened.Remote)
	echo(t, opened.Local)
	// A stalled TLS connection must not prevent another client from connecting.
	stalled, err := net.Dial("tcp", cfg.Server.RemoteAddr)
	require.NoError(t, err)
	defer stalled.Close()
	for _, endpoint := range []struct{ name, address, ca, identity string }{
		{"remote", cfg.Server.RemoteAddr, cfg.TLS.CAFile, cfg.Host.ID},
		{"local", cfg.Server.LocalAddr, "", ""},
	} {
		for _, payload := range []struct {
			name          string
			serialization int
			body          []byte
		}{
			{"PB", codec.SerializationTypePB, []byte{0x0a, 0x01, 'a', 0x0a, 0x01, 'b'}},
			{"JSON", codec.SerializationTypeJSON, []byte(" { \"name\": \"keep bytes\", \"n\": 1 }\n")},
		} {
			t.Run(endpoint.name+"/"+payload.name, func(t *testing.T) {
				response, err := roundTrip(endpoint.address, endpoint.ca, endpoint.identity, payload.serialization, payload.body)
				require.NoError(t, err)
				require.Equal(t, payload.body, response)
			})
		}
	}
	// Reject wrong trust and identity after successful calls to this address.
	otherCA := testcert.Files(t, testcert.New(t, nil), "storage", nil).CAFile
	for _, invalid := range []struct{ name, ca, identity string }{
		{"plaintext", "", ""}, {"wrong CA", otherCA, "storage"}, {"wrong host", cfg.TLS.CAFile, "compute"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			_, err := roundTrip(cfg.Server.RemoteAddr, invalid.ca, invalid.identity, codec.SerializationTypePB, []byte{0x0a, 0x01, 'a'})
			require.Error(t, err)
		})
	}
	// The validated key remains in use even if the source file is replaced.
	testcert.Write(t, cfg.TLS.KeyFile, []byte("changed fixture"))
	_, err = roundTrip(cfg.Server.RemoteAddr, cfg.TLS.CAFile, "storage", codec.SerializationTypePB, []byte{0x0a, 0x01, 'a'})
	require.NoError(t, err)
}

func TestListenerRollbackAndIdentityValidation(t *testing.T) {
	for _, busyIndex := range []int{0, 1, 2} {
		t.Run([]string{"remote", "local", "health"}[busyIndex], func(t *testing.T) {
			cfg, material := fixture(t)
			addresses := []string{cfg.Server.RemoteAddr, cfg.Server.LocalAddr, cfg.Server.HealthAddr}
			busy, err := net.Listen("tcp", addresses[busyIndex])
			require.NoError(t, err)
			defer busy.Close()
			_, err = Open(context.Background(), cfg, material)
			require.Error(t, err)
			for _, address := range addresses[:busyIndex] {
				probe, err := net.Listen("tcp", address)
				require.NoError(t, err, "startup must release earlier listeners")
				require.NoError(t, probe.Close())
			}
		})
	}
	cfg, material := fixture(t)
	_, err := Open(context.Background(), cfg, nil)
	require.Error(t, err)
	cfg.Host.ID, cfg.Control.Caller = "compute", "host-gateway@compute"
	_, err = Open(context.Background(), cfg, material)
	require.ErrorContains(t, err, "for this host")
	cfg, material = fixture(t)
	cfg.Server.LocalAddr = "0.0.0.0:11002"
	_, err = Open(context.Background(), cfg, material)
	require.ErrorContains(t, err, "loopback")
	cfg, material = fixture(t)
	cfg.Server.HealthAddr = cfg.Server.RemoteAddr
	_, err = Open(context.Background(), cfg, material)
	require.ErrorContains(t, err, "distinct")
}

func TestTLSMinimumVersionAndStalledHandshakeBound(t *testing.T) {
	cfg, material := fixture(t)
	opened, err := Open(context.Background(), cfg, material)
	require.NoError(t, err)
	defer opened.Close()
	readResult := make(chan error, 1)
	go func() {
		accepted, err := opened.Remote.Accept()
		if err != nil {
			readResult <- err
			return
		}
		defer accepted.Close()
		_, err = accepted.Read(make([]byte, 1))
		readResult <- err
	}()
	conn, err := net.Dial("tcp", cfg.Server.RemoteAddr)
	require.NoError(t, err)
	defer conn.Close()
	select {
	case err := <-readResult:
		require.Error(t, err)
	case <-time.After(handshakeTimeout + 3*time.Second):
		t.Fatal("stalled TLS handshake exceeded its time limit")
	}
	// Use an actual old TLS client to exercise the server's minimum version.
	go func() {
		accepted, err := opened.Remote.Accept()
		if err == nil {
			defer accepted.Close()
			_, _ = io.Copy(io.Discard, accepted)
		}
	}()
	clientConfig, err := material.Client("storage")
	require.NoError(t, err)
	clientConfig.MinVersion, clientConfig.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
	client, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", cfg.Server.RemoteAddr, clientConfig)
	if client != nil {
		_ = client.Close()
	}
	require.Error(t, err)
	_, err = os.Stat(cfg.Store.Path)
	require.ErrorIs(t, err, os.ErrNotExist, "opening sockets must not initialize caches or credentials")
}

func TestClosingListenersCancelsPendingTLSHandshake(t *testing.T) {
	cfg, material := fixture(t)
	opened, err := Open(context.Background(), cfg, material)
	require.NoError(t, err)
	t.Cleanup(func() { _ = opened.Close() })
	accepted := make(chan struct{})
	readResult := make(chan error, 1)
	go func() {
		conn, err := opened.Remote.Accept()
		if err != nil {
			readResult <- err
			return
		}
		defer conn.Close()
		close(accepted)
		_, err = conn.Read(make([]byte, 1))
		readResult <- err
	}()
	conn, err := net.Dial("tcp", cfg.Server.RemoteAddr)
	require.NoError(t, err)
	defer conn.Close()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("TLS connection was not accepted")
	}
	require.NoError(t, opened.Close())
	select {
	case err := <-readResult:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("listener shutdown did not cancel the TLS handshake")
	}
}
