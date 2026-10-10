package gatewayio

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type gatewayWire struct {
	pb.UnimplementedPrimaryStore
	directorypb.UnimplementedDirectory
	credentials gatewayauth.Credentials
	mu          sync.Mutex
	host        string
	calls       map[string]int
	nonces      map[string]bool
}

func (w *gatewayWire) GetDirectory(_ context.Context, _ *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d := servicecatalog.Directory{Services: map[string][]string{"trpc.moox.storage.PrimaryStore": {w.host}}, Hosts: map[string]servicecatalog.DirectoryHost{w.host: {Address: "public-address-must-not-be-dialed.example.test"}}}
	version, err := d.VersionHash()
	return &directorypb.GetDirectoryRsp{Changed: true, Version: version, Services: map[string]*directorypb.ServiceHosts{"trpc.moox.storage.PrimaryStore": {HostIds: []string{w.host}}}, Hosts: map[string]*directorypb.DirectoryHost{w.host: {Address: d.Hosts[w.host].Address}}}, err
}

func (w *gatewayWire) verify(ctx context.Context, method string, request proto.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	headers := http.Header{}
	for key, value := range codec.Message(ctx).ServerMetaData() {
		headers.Set(key, string(value))
	}
	body, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	service := "trpc.moox.storage.PrimaryStore"
	claims, err := gatewayauth.Verify(w.credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: w.host, Callee: service, Func: method, Body: body}, headers, time.Now())
	if err != nil {
		return err
	}
	role := request.(interface{ GetAuthInfo() *pb.AuthInfo }).GetAuthInfo()
	if role.GetAppId() != "storage-role" || role.GetAppKey() != "role-key" || w.nonces[claims.Nonce] {
		return errors.New("Storage role changed or nonce reused")
	}
	w.nonces[claims.Nonce] = true
	w.calls[method]++
	if method == "ReadTimeSeriesRows" && w.calls[method] == 1 {
		w.host = "storage-next"
		return errs.NewFrameError(errs.RetServerNoService, "refresh directory")
	}
	if method == "EnsureDatasetPeriod" {
		return errs.NewFrameError(errs.RetServerNoService, "write must be sent once")
	}
	return nil
}

func (w *gatewayWire) ReadTimeSeriesRows(ctx context.Context, request *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, w.verify(ctx, "ReadTimeSeriesRows", request)
}

func (w *gatewayWire) EnsureDatasetPeriod(ctx context.Context, request *pb.PrimaryEnsureDatasetPeriodReq) (*pb.PrimaryEnsureDatasetPeriodRsp, error) {
	return nil, w.verify(ctx, "EnsureDatasetPeriod", request)
}

func TestSignedGatewayUsesTrustedSSHTunnelsRefreshesRoutesAndCloses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	wire := &gatewayWire{credentials: gatewayauth.Credentials{Caller: "moox-cli", KeyID: "admin-assigned-operator-42", Secret: strings.Repeat("operator-secret-", 4)}, host: "storage", calls: map[string]int{}, nonces: map[string]bool{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	pb.RegisterPrimaryStoreService(svc, wire)
	directorypb.RegisterDirectoryService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	root := t.TempDir()
	knownHosts := filepath.Join(root, "known_hosts")
	require.NoError(t, os.WriteFile(knownHosts, nil, 0o600))
	snapshot := &setupconfig.Snapshot{}
	testfixture.SetHost(&snapshot.Manifest, testfixture.GatewaySSH(t, "control", listener.Addr().String(), knownHosts), "admin", "console-proxy", "web-host", "eventbus")
	testfixture.SetHost(&snapshot.Manifest, testfixture.GatewaySSH(t, "storage", listener.Addr().String(), knownHosts), "storage-primary")
	testfixture.SetHost(&snapshot.Manifest, testfixture.GatewaySSH(t, "storage-next", listener.Addr().String(), knownHosts))
	identity := filepath.Join(root, "gateway-client.yaml")
	require.NoError(t, os.WriteFile(identity, []byte("caller: moox-cli\nkey_id: "+wire.credentials.KeyID+"\nkey_file: caller-moox-cli.key\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "caller-moox-cli.key"), []byte(wire.credentials.Secret+"\n"), 0o600))
	client, err := open(t.Context(), snapshot, identity, setupssh.Options{KnownHostsPath: knownHosts, Timeout: time.Second, DisableAgent: true, IdentityFiles: []string{filepath.Join(root, "no-default-key")}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	auth := &pb.AuthInfo{AppId: "storage-role", AppKey: "role-key"}
	require.NoError(t, client.Invoke(t.Context(), "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", &pb.ReadTimeSeriesRowsReq{AuthInfo: auth}, &pb.ReadTimeSeriesRowsRsp{}))
	require.Error(t, client.Invoke(t.Context(), "trpc.moox.storage.PrimaryStore", "EnsureDatasetPeriod", &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth}, &pb.PrimaryEnsureDatasetPeriodRsp{}))
	wire.mu.Lock()
	readCalls, writeCalls, nonceCount := wire.calls["ReadTimeSeriesRows"], wire.calls["EnsureDatasetPeriod"], len(wire.nonces)
	wire.mu.Unlock()
	require.Equal(t, 2, readCalls)
	require.Equal(t, 1, writeCalls)
	require.Equal(t, 3, nonceCount)
	client.tunnels.mu.Lock()
	oldControl := client.tunnels.connections["control"]
	client.tunnels.mu.Unlock()
	require.NoError(t, oldControl.ssh.Close())
	reopenedAddress, reopenErr := client.tunnels.Resolve(t.Context(), "control")
	require.NoError(t, reopenErr)
	require.NotEqual(t, oldControl.listener.Addr().String(), reopenedAddress)
	require.NoError(t, client.Refresh(t.Context()), "a broken control tunnel must be reopened before refreshing Directory")
	client.tunnels.mu.Lock()
	newControlAddress := client.tunnels.connections["control"].listener.Addr().String()
	client.tunnels.mu.Unlock()
	require.NotEqual(t, oldControl.listener.Addr().String(), newControlAddress)
	_, err = client.tunnels.Resolve(t.Context(), "unconfigured")
	require.ErrorContains(t, err, "no configured SSH identity")
	client.tunnels.mu.Lock()
	connectionCount := len(client.tunnels.connections)
	address := client.tunnels.connections["storage-next"].listener.Addr().String()
	client.tunnels.mu.Unlock()
	require.Equal(t, 3, connectionCount)
	require.NoError(t, client.Close())
	require.NoError(t, client.Close())
	_, err = net.DialTimeout("tcp", address, time.Second)
	require.Error(t, err)
	_, err = client.tunnels.Resolve(t.Context(), "storage-next")
	require.ErrorIs(t, err, net.ErrClosed)
	require.Error(t, client.Invoke(t.Context(), "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", &pb.ReadTimeSeriesRowsReq{AuthInfo: auth}, &pb.ReadTimeSeriesRowsRsp{}))
	// Host-key verification remains mandatory before even querying Directory.
	require.NoError(t, os.WriteFile(knownHosts, nil, 0o600))
	_, err = open(t.Context(), snapshot, identity, setupssh.Options{KnownHostsPath: knownHosts, Timeout: time.Second, DisableAgent: true})
	require.ErrorContains(t, err, "host_key_unknown")
}

func TestOperatorIdentityRejectsUnsafeAndObsoleteConfiguration(t *testing.T) {
	base := "caller: moox-cli\nkey_id: admin-generated-key\nkey_file: caller-moox-cli.key\n"
	for _, raw := range []string{strings.Replace(base, "moox-cli", "collector", 1), strings.Replace(base, "admin-generated-key", "", 1), strings.Replace(base, "admin-generated-key", "invalid/key", 1), base + "target: ip://public.example:11003\n", base + "caller: moox-cli\n", base + "---\n{}\n"} {
		path := filepath.Join(t.TempDir(), "identity.yaml")
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		_, err := loadIdentity(path)
		require.Error(t, err)
	}
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte(base), 0o644))
	_, err := loadIdentity(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0o600))
	link := path + ".link"
	require.NoError(t, os.Symlink(path, link))
	_, err = loadIdentity(link)
	require.Error(t, err)
}
