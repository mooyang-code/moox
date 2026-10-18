//go:build linux && hostgateway_e2e

package gatewaycontrol

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const primaryPath = "trpc.moox.storage.PrimaryStore"

// This gate runs production gateway executables against the real Admin service,
// SQLite topology/key store, durable nonces and deployment PKI. Separate Linux
// loopback IPs preserve the canonical remote port without endpoint overrides.
// Both pure Go executables may be cross-compiled on the developer's machine.
func TestHostGatewayRealAdminE2E(t *testing.T) {
	binary := requiredE2EFile(t, "MOOX_HOST_GATEWAY_E2E_BINARY")
	framework := requiredE2EFile(t, "MOOX_HOST_GATEWAY_E2E_TRPC_CONFIG")
	f := newControlFixture(t)
	f.service.now = time.Now
	ctx := context.Background()
	controlSpec := sysdeploy.HostSpec{HostID: "control", Address: "127.0.0.1", Components: []string{"admin", "console-proxy", "web-host", "collector"}}
	storageSpec := sysdeploy.HostSpec{HostID: "storage", Address: "127.0.0.2", Components: []string{"storage-primary", "storage-node", "storage-view"}}
	require.NoError(t, f.topology.SyncHosts(ctx, []sysdeploy.HostSpec{controlSpec, storageSpec}))

	opened, err := net.Listen("tcp", "127.0.0.1:11112")
	require.NoError(t, err)
	controlService := server.New(server.WithServiceName(servicecatalog.GatewayControlPath), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithListener(opened), server.WithAddress(opened.Addr().String()), ServerOption())
	require.NoError(t, Register(controlService, f.service))
	go func() { _ = controlService.Serve() }()
	t.Cleanup(func() { _ = controlService.Close(nil) })
	var backendCalls atomic.Int64
	startEchoBackend(t, &backendCalls)

	root := t.TempDir()
	ca, err := pki.Open(filepath.Join(root, "pki"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ca.Close()) })
	_, err = ca.EnsureCA()
	require.NoError(t, err)
	configs := map[string]hostgatewayconfig.Config{}
	for _, host := range []struct{ id, ip string }{{"control", "127.0.0.1"}, {"storage", "127.0.0.2"}} {
		key, err := f.keys.Current(ctx, "host-gateway@"+host.id)
		require.NoError(t, err)
		issued, err := ca.Issue(pki.HostIdentity{HostID: host.id, Address: host.ip}, filepath.Join(root, host.id, "identity"))
		require.NoError(t, err)
		cfg := hostgatewayconfig.Default(host.id, "control", "127.0.0.1", key.KeyID)
		cfg.Server = hostgatewayconfig.Server{LocalAddr: net.JoinHostPort(host.ip, "11002"), RemoteAddr: net.JoinHostPort(host.ip, "11003"), HealthAddr: net.JoinHostPort(host.ip, "11012")}
		cfg.TLS = hostgatewayconfig.TLS{CertificateFile: issued.Certificate, KeyFile: issued.Key, CAFile: issued.CA}
		cfg.Store.Path, cfg.Control.KeyFile = filepath.Join(root, host.id, "cache"), filepath.Join(root, host.id, "caller.key")
		require.NoError(t, os.WriteFile(cfg.Control.KeyFile, []byte(key.Credentials().Secret+"\n"), 0o600))
		configs[host.id] = cfg
	}
	controlProcess := startGatewayExecutable(t, binary, framework, configs["control"])
	storageProcess := startGatewayExecutable(t, binary, framework, configs["storage"])
	waitForE2E(t, 15*time.Second, "both actual gateway heartbeats", func() bool {
		for _, host := range []string{"control", "storage"} {
			status, err := f.topology.GetGatewayStatus(ctx, host)
			if err != nil || status.InstanceID == "" || status.AppliedHash != status.ExpectedHash || status.Version != "control-e2e" {
				return false
			}
		}
		return true
	})
	initialInstance := f.status(t).InstanceID
	initialControlInstance := controlProcess.instance(t, f)

	collector, err := f.keys.Current(ctx, "collector")
	require.NoError(t, err)
	console, err := f.keys.Current(ctx, "console")
	require.NoError(t, err)
	clients := map[string]*gatewayclient.Client{}
	for _, host := range []string{"control", "storage"} {
		cfg := configs[host]
		c, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: collector.Credentials(), LocalHostID: host, LocalAddress: cfg.Server.LocalAddr, CAFile: cfg.TLS.CAFile, Serialization: codec.SerializationTypePB, Timeout: 2 * time.Second})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, c.Close()) })
		clients[host] = c
		var result wrapperspb.StringValue
		require.NoError(t, c.Invoke(ctx, primaryPath, "ReadTimeSeriesRows", wrapperspb.String("object through "+host), &result))
		require.Equal(t, "object through "+host, result.Value)
		for _, payload := range []struct {
			serialization int
			raw           []byte
		}{
			{codec.SerializationTypePB, []byte{0x0a, 1, 'a', 0x0a, 1, 'b'}},
			{codec.SerializationTypeJSON, []byte(" { \"value\" : \"raw JSON\" }\n")},
		} {
			result, err := c.Forward(ctx, primaryPath, "ReadTimeSeriesRows", payload.serialization, payload.raw)
			require.NoError(t, err)
			require.Equal(t, payload.raw, result)
		}
		browser, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: console.Credentials(), LocalHostID: host, LocalAddress: cfg.Server.LocalAddr, CAFile: cfg.TLS.CAFile, Serialization: codec.SerializationTypeJSON, Timeout: 2 * time.Second})
		require.NoError(t, err)
		raw := []byte(" { \"fixture\" : true }\n")
		resultJSON, err := browser.Forward(ctx, primaryPath, "UpsertFields", codec.SerializationTypeJSON, raw)
		require.NoError(t, err)
		require.Equal(t, raw, resultJSON)
		require.NoError(t, browser.Close())
	}
	t.Log("SCENARIO PASS object-and-exact-raw-pb-json-local-and-tls")

	storageTLS := e2eTrust(t, configs["storage"].TLS.CAFile, "storage")
	body := []byte{0x0a, 1, 'x'}
	headers := signedE2E(t, collector.Credentials(), "storage", primaryPath, "ReadTimeSeriesRows", body)
	_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, storageTLS, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, headers)
	require.NoError(t, err)
	_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, storageTLS, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, headers)
	require.ErrorContains(t, err, "replayed")
	_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, collector.Credentials(), "storage", primaryPath, "ReadTimeSeriesRows", body))
	require.Error(t, err, "remote listener must reject plaintext")
	wrongCA, err := pki.Open(filepath.Join(root, "wrong-pki"))
	require.NoError(t, err)
	_, err = wrongCA.EnsureCA()
	require.NoError(t, err)
	_, err = wrongCA.ExportCA(filepath.Join(root, "wrong-public"))
	require.NoError(t, err)
	require.NoError(t, wrongCA.Close())
	_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, e2eTrust(t, filepath.Join(root, "wrong-public", "moox-ca.crt"), "storage"), primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, headers)
	require.Error(t, err, "a different CA must not reuse the successful TLS connection")
	_, err = e2eRawCall(configs["control"].Server.LocalAddr, nil, "trpc.moox.admin.CollectorPublishLease", "ValidateCollectorPublishLease", codec.SerializationTypePB, nil, signedE2E(t, console.Credentials(), "control", "trpc.moox.admin.CollectorPublishLease", "ValidateCollectorPublishLease", nil))
	require.ErrorContains(t, err, "not allowed", "console must be denied at the gateway itself")
	t.Log("SCENARIO PASS tls-trust-plaintext-acl-and-durable-replay")

	// A newly registered host is also a new caller. Its signature must become
	// accepted through the already running control gateway after the next pull.
	require.NoError(t, f.topology.SyncHostPlacements(ctx, sysdeploy.HostSpec{HostID: "joining", Address: "127.0.0.3"}))
	joining, err := f.keys.Ensure(ctx, "host-gateway@joining")
	require.NoError(t, err)
	joiningBody, err := codec.Marshal(codec.SerializationTypePB, &pb.PullSnapshotReq{HostId: "joining"})
	require.NoError(t, err)
	waitForE2E(t, 15*time.Second, "new caller accepted without gateway restart", func() bool {
		raw, err := e2eRawCall(configs["control"].Server.RemoteAddr, e2eTrust(t, configs["control"].TLS.CAFile, "control"), servicecatalog.GatewayControlPath, "PullSnapshot", codec.SerializationTypePB, joiningBody, signControl(t, joining.Credentials(), "PullSnapshot", "control", joiningBody))
		if err != nil {
			return false
		}
		var rsp pb.PullSnapshotRsp
		return codec.Unmarshal(codec.SerializationTypePB, raw, &rsp) == nil && rsp.Changed && rsp.Snapshot.GetHostId() == "joining"
	})
	controlStatus, err := f.topology.GetGatewayStatus(ctx, "control")
	require.NoError(t, err)
	require.Equal(t, initialControlInstance, controlStatus.InstanceID)
	t.Log("SCENARIO PASS new-caller-next-snapshot-without-restart")

	require.NoError(t, f.topology.SetPlacementStatus(ctx, "storage", "storage-primary", servicecatalog.Disabled))
	waitForE2E(t, 15*time.Second, "disabled placement explicitly rejected", func() bool {
		_, err := clients["control"].Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
		return err != nil && (strings.Contains(err.Error(), "no enabled deployment") || strings.Contains(err.Error(), "not deployed"))
	})
	require.NoError(t, f.topology.SyncHosts(ctx, []sysdeploy.HostSpec{
		{HostID: "storage", Address: "127.0.0.2", Components: []string{"storage-node", "storage-view"}},
		{HostID: "control", Address: "127.0.0.1", Components: append(slices.Clone(controlSpec.Components), "storage-primary")},
	}))
	waitForE2E(t, 15*time.Second, "existing clients automatically switch deployment target", func() bool {
		for _, c := range clients {
			if !slices.Equal(c.Directory().Services[primaryPath], []string{"control"}) {
				return false
			}
			result, err := c.Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
			if err != nil || !slices.Equal(result, body) {
				return false
			}
		}
		return true
	})
	// The old gateway must also withdraw its local route, independent of what
	// the caller's Directory already knows.
	waitForE2E(t, 15*time.Second, "old gateway route withdrawn", func() bool {
		_, err := e2eRawCall(configs["storage"].Server.LocalAddr, nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, collector.Credentials(), "storage", primaryPath, "ReadTimeSeriesRows", body))
		return err != nil && strings.Contains(err.Error(), "not deployed")
	})
	t.Log("SCENARIO PASS placement-withdrawal-and-automatic-target-switch")

	storageProcess.stop(t)
	storageProcess = startGatewayExecutable(t, binary, framework, configs["storage"])
	waitForE2E(t, 15*time.Second, "normal process replacement heartbeat", func() bool { return f.status(t).InstanceID != initialInstance })
	require.Empty(t, f.status(t).ConflictInstanceID)
	require.Equal(t, initialInstance, f.status(t).PreviousInstanceID)
	// A second genuine process uses separate sockets/cache but the same host
	// identity. Its real timer and the original timer must reveal the conflict.
	duplicate := configs["storage"]
	duplicate.Server = hostgatewayconfig.Server{RemoteAddr: unusedE2EAddress(t), LocalAddr: unusedE2EAddress(t), HealthAddr: unusedE2EAddress(t)}
	duplicate.Store.Path = filepath.Join(root, "duplicate-cache")
	duplicateProcess := startGatewayExecutable(t, binary, framework, duplicate)
	waitForE2E(t, 31*time.Second, "alternating real processes detected as conflict", func() bool { return f.status(t).ConflictInstanceID != "" })
	duplicateProcess.stop(t)
	t.Log("SCENARIO PASS normal-restart-and-live-instance-conflict")

	// Keep control's business route active, but stop the actual Admin control
	// listener. Both gateways retain their previously authenticated snapshots.
	require.NoError(t, controlService.Close(nil))
	require.Equal(t, http.StatusOK, e2eHealth(t, configs["control"].Server.HealthAddr))
	before := backendCalls.Load()
	result, err := clients["storage"].Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
	require.NoError(t, err)
	require.Equal(t, body, result)
	require.Greater(t, backendCalls.Load(), before)
	waitForE2E(t, 94*time.Second, "90-second stale control readiness", func() bool {
		return e2eHealth(t, configs["control"].Server.HealthAddr) == http.StatusServiceUnavailable
	})
	result, err = clients["storage"].Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
	require.NoError(t, err)
	require.Equal(t, body, result)
	controlProcess.stop(t)
	controlProcess = startGatewayExecutable(t, binary, framework, configs["control"])
	require.Equal(t, http.StatusServiceUnavailable, e2eHealth(t, configs["control"].Server.HealthAddr))
	// A fresh client reads the restarted gateway's cached Directory, then uses
	// cached routes and verification keys while Admin is still unavailable.
	restarted, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: collector.Credentials(), LocalHostID: "control", LocalAddress: configs["control"].Server.LocalAddr, CAFile: configs["control"].TLS.CAFile, Serialization: codec.SerializationTypePB, Timeout: 2 * time.Second})
	require.NoError(t, err)
	defer restarted.Close()
	result, err = restarted.Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
	require.NoError(t, err)
	require.Equal(t, body, result)
	t.Log("SCENARIO PASS offline-routing-90-second-readiness-and-cache-restart")
}

func requiredE2EFile(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	require.NotEmpty(t, path, "explicit e2e gate requires %s; do not skip it", name)
	require.True(t, filepath.IsAbs(path), "%s must be absolute", name)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	return path
}

func waitForE2E(t *testing.T, budget time.Duration, reason string, check func() bool) {
	t.Helper()
	require.Eventually(t, check, budget, 100*time.Millisecond, reason)
}

type gatewayExecutable struct {
	cmd     *exec.Cmd
	done    chan error
	stopped bool
	log     string
	host    string
}

func startGatewayExecutable(t *testing.T, binary, framework string, cfg hostgatewayconfig.Config) *gatewayExecutable {
	t.Helper()
	dir := t.TempDir()
	raw, err := hostgatewayconfig.Encode(cfg)
	require.NoError(t, err)
	path := filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	output, err := os.OpenFile(filepath.Join(dir, "process.log"), os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	cmd := exec.Command(binary, "--config", path, "--conf", framework)
	cmd.Env = append(os.Environ(), "MOOX_HEALTH_AUTH_VERSION=moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY=fixture", "MOOX_HEALTH_AUTH_SECRET_KEY=synthetic-health", "MOOX_INSTANCE_ID="+cfg.Host.ID, "MOOX_NODE_ID="+cfg.Host.ID, "MOOX_BOOT_ID=synthetic-boot", "MOOX_METRICS_EVENTBUS_URL=nats://127.0.0.1:1", "MOOX_GATEWAY_ROUTE_SYNC_STALE_AFTER_SECONDS=90")
	cmd.Stdout, cmd.Stderr = output, output
	require.NoError(t, cmd.Start())
	p := &gatewayExecutable{cmd: cmd, done: make(chan error, 1), log: output.Name(), host: cfg.Host.ID}
	go func() { err := cmd.Wait(); _ = output.Close(); p.done <- err }()
	t.Cleanup(func() { p.stop(t) })
	source, err := gatewayclient.NewLocalDirectorySource(cfg.Server.LocalAddr, 300*time.Millisecond)
	require.NoError(t, err)
	defer source.Close()
	waitForE2E(t, 15*time.Second, "gateway process serves Directory", func() bool { _, err := source.Fetch(context.Background(), ""); return err == nil })
	return p
}

func (p *gatewayExecutable) stop(t *testing.T) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-p.done:
		if err != nil {
			output, _ := os.ReadFile(p.log)
			t.Errorf("gateway %s failed: %v\n%s", p.host, err, output)
		}
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		t.Errorf("gateway %s failed to stop", p.host)
	}
}

func (p *gatewayExecutable) instance(t *testing.T, f *controlFixture) string {
	t.Helper()
	status, err := f.topology.GetGatewayStatus(context.Background(), p.host)
	require.NoError(t, err)
	return status.InstanceID
}

func unusedE2EAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := l.Addr().String()
	require.NoError(t, l.Close())
	return address
}

func startEchoBackend(t *testing.T, calls *atomic.Int64) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:20102")
	require.NoError(t, err)
	svc := server.New(server.WithServiceName(primaryPath), server.WithListener(l), server.WithAddress(l.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTransport(transport.NewServerTransport()), server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	require.NoError(t, svc.Register(&server.ServiceDesc{ServiceName: primaryPath, HandlerType: (*interface{})(nil), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
		raw := &codec.Body{}
		filters, err := f(raw)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, raw, func(ctx context.Context, body interface{}) (interface{}, error) {
			for key := range codec.Message(ctx).ServerMetaData() {
				if strings.HasPrefix(strings.ToLower(key), "x-moox-") {
					return nil, errors.New("gateway leaked authentication metadata")
				}
			}
			calls.Add(1)
			return body, nil
		})
	}}}}, struct{}{}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
}

func signedE2E(t *testing.T, credential gatewayauth.Credentials, host, service, method string, body []byte) http.Header {
	t.Helper()
	headers, err := gatewayauth.Sign(credential, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: host, Callee: service, Func: method, Body: body}, time.Now())
	require.NoError(t, err)
	return headers
}

func e2eTrust(t *testing.T, path, host string) *tls.Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(raw))
	return &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}
}

func e2eRawCall(address string, trust *tls.Config, service, method string, serialization int, body []byte, headers http.Header) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx, msg := codec.WithNewMessage(ctx)
	defer codec.PutBackMessage(msg)
	msg.WithClientRPCName("/" + service + "/" + method)
	// Calls are synchronous and every call owns its transport and connections.
	// No successful TLS session can be reused for a subsequent wrong-CA call.
	var connections []net.Conn
	var connectionMu sync.Mutex
	closed := false
	pool := connpool.NewConnectionPool(connpool.WithDialFunc(func(opts *connpool.DialOptions) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: time.Second}
		var connection net.Conn
		var err error
		if trust == nil {
			connection, err = dialer.Dial(opts.Network, opts.Address)
		} else {
			connection, err = tls.DialWithDialer(dialer, opts.Network, opts.Address, trust)
		}
		if err == nil {
			connectionMu.Lock()
			defer connectionMu.Unlock()
			if closed {
				_ = connection.Close()
				return nil, net.ErrClosed
			}
			connections = append(connections, connection)
		}
		return connection, err
	}))
	defer func() {
		connectionMu.Lock()
		closed = true
		owned := slices.Clone(connections)
		connectionMu.Unlock()
		for _, c := range owned {
			_ = c.Close()
		}
	}()
	options := []client.Option{client.WithTarget("ip://" + address), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithServiceName(service), client.WithCalleeMethod(method), client.WithSerializationType(serialization), client.WithCurrentSerializationType(codec.SerializationTypeNoop), client.WithTransport(transport.NewClientTransport()), client.WithPool(pool), client.WithMultiplexed(false), client.WithTimeout(time.Second), client.WithDialTimeout(time.Second)}
	for key, values := range headers {
		options = append(options, client.WithMetaData(key, []byte(values[0])))
	}
	result := &codec.Body{}
	err := client.DefaultClient.Invoke(ctx, &codec.Body{Data: body}, result, options...)
	return result.Data, err
}

func e2eHealth(t *testing.T, address string) int {
	t.Helper()
	random := make([]byte, 32)
	_, err := rand.Read(random)
	require.NoError(t, err)
	nonce, now := hex.EncodeToString(random), time.Now().Unix()
	signature, err := requestauth.Sign("synthetic-health", requestauth.Material{Method: http.MethodGet, Path: "/readyz", Timestamp: now, Nonce: nonce})
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/readyz", nil)
	require.NoError(t, err)
	request.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("moox-health-v1/fixture/%d/%s/%s", now, nonce, signature))
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode
}
