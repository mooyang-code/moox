//go:build e2e_external

package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/listener"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/commonpb"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const ownerTradePath = "trpc.moox.trade.TradeConsoleService"
const ownerHostID = "gateway-owner-e2e"

type ownerDirectory struct {
	directorypb.UnimplementedDirectory
	directory servicecatalog.Directory
}

func (s *ownerDirectory) GetDirectory(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	if req.GetCurrentVersion() == s.directory.Version {
		return &directorypb.GetDirectoryRsp{Version: s.directory.Version}, nil
	}
	return &directorypb.GetDirectoryRsp{Version: s.directory.Version, Changed: true,
		Services: map[string]*directorypb.ServiceHosts{ownerTradePath: {HostIds: []string{ownerHostID}}},
		Hosts:    map[string]*directorypb.DirectoryHost{ownerHostID: {Address: "127.0.0.1"}}}, nil
}

func ownerCertificates(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{ownerHostID}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pair
}

func TestExternalGatewayOwnerHandler(t *testing.T) {
	coord := os.Getenv("MOOX_GATEWAY_OWNER_E2E_COORD")
	require.NotEmpty(t, coord)
	read := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join(coord, name))
		require.NoError(t, err)
		return raw
	}
	write := func(name string, body []byte) {
		require.NoError(t, os.WriteFile(filepath.Join(coord, name), body, 0600))
	}
	caPEM, pair := ownerCertificates(t)
	write("gateway-ca.pem", caPEM)
	unrelated, _ := ownerCertificates(t)
	write("unrelated-ca.pem", unrelated)
	credentials := testsnapshot.Credential("strategy")
	write("caller-key-id", []byte(credentials.KeyID))
	write("caller-key", []byte(credentials.Secret))
	raw := testsnapshot.New(t, ownerHostID, "trade")
	require.Equal(t, "127.0.0.1:11200", string(read("trade-ready")))
	testsnapshot.Rehash(t, raw)
	view, err := snapshot.Build(ownerHostID, raw)
	require.NoError(t, err)
	state := &snapshot.State{}
	state.Apply(view)
	nonces, err := store.OpenNonces(filepath.Join(t.TempDir(), "nonces"))
	require.NoError(t, err)
	defer nonces.Close()
	proxy, err := NewService(ServiceOptions{State: state, Nonces: nonces})
	require.NoError(t, err)
	defer proxy.Close()
	opened, err := tls.Listen("tcp", "127.0.0.1:11003", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	require.NoError(t, err, "native peer contract uses port 11003; the isolated E2E requires that loopback port free")
	svc := listener.TRPC(opened, "owner-gateway")
	require.NoError(t, proxy.Register(svc))
	serve := func(svc server.Service) {
		done := make(chan error, 1)
		go func() { done <- svc.Serve() }()
		t.Cleanup(func() {
			_ = svc.Close(nil)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("native E2E server did not stop")
			}
		})
	}
	serve(svc)
	directory := servicecatalog.Directory{Services: map[string][]string{ownerTradePath: {ownerHostID}}, Hosts: map[string]servicecatalog.DirectoryHost{ownerHostID: {Address: "127.0.0.1"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	local, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	directoryService := server.New(server.WithListener(local), server.WithAddress(local.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTransport(transport.NewServerTransport()))
	directorypb.RegisterDirectoryService(directoryService, &ownerDirectory{directory: directory})
	serve(directoryService)
	write("directory-ready", []byte(local.Addr().String()))
	write("gateway-ready", []byte(opened.Addr().String()))
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(coord, "strategy-done")); return err == nil }, 45*time.Second, 25*time.Millisecond)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caPEM))
	trust := &tls.Config{RootCAs: roots, ServerName: ownerHostID, MinVersion: tls.VersionTLS12}
	ctx := t.Context()
	headers, err := testrpc.Signed(credentials, ownerHostID, ownerTradePath, "SubmitOrder", nil)
	require.NoError(t, err)
	_, err = testrpc.Call(ctx, opened.Addr().String(), trust, ownerTradePath, "SubmitOrder", codec.SerializationTypePB, nil, headers)
	require.ErrorContains(t, err, "not allowed", "server must reject a caller that bypasses the client ACL")
	headers, err = testrpc.Signed(credentials, "wrong-node", ownerTradePath, "GetLogicalAccount", nil)
	require.NoError(t, err)
	_, err = testrpc.Call(ctx, opened.Addr().String(), trust, ownerTradePath, "GetLogicalAccount", codec.SerializationTypePB, nil, headers)
	require.ErrorContains(t, err, "authentication failed")
	headers, err = testrpc.Signed(credentials, ownerHostID, ownerTradePath, "GetLogicalAccount", nil)
	require.NoError(t, err)
	headers.Set("X-Space-Id", "space-gateway-e2e")
	_, err = testrpc.Call(ctx, opened.Addr().String(), trust, ownerTradePath, "GetLogicalAccount", codec.SerializationTypePB, nil, headers)
	require.NoError(t, err)
	_, err = testrpc.Call(ctx, opened.Addr().String(), trust, ownerTradePath, "GetLogicalAccount", codec.SerializationTypePB, nil, headers)
	require.ErrorContains(t, err, "replayed")
	wrongTrust := trust.Clone()
	wrongTrust.ServerName = "wrong-node"
	_, err = testrpc.Call(ctx, opened.Addr().String(), wrongTrust, ownerTradePath, "GetLogicalAccount", codec.SerializationTypePB, nil, headers)
	require.Error(t, err, "private CA must verify the intended host identity")
	// Exercise the CLI deployment probe's JSON-over-native contract against
	// the actual Trade handler, including ambiguous space metadata.
	checkPermission := func(method string, body []byte, ambiguousSpace bool) {
		headers, err := testrpc.Signed(testsnapshot.Credential("moox-cli"), ownerHostID, ownerTradePath, method, body)
		require.NoError(t, err)
		if ambiguousSpace {
			headers["X-Space-Id"] = []string{"space-gateway-e2e"}
			headers["x-space-id"] = []string{"another-space"}
		}
		response, err := testrpc.Call(ctx, opened.Addr().String(), trust, ownerTradePath, method, codec.SerializationTypeJSON, body, headers)
		require.NoError(t, err)
		var envelope map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(response, &envelope))
		status := envelope["retInfo"]
		if len(status) == 0 {
			status = envelope["ret_info"]
		}
		var retInfo commonpb.RetInfo
		require.NoError(t, protojson.Unmarshal(status, &retInfo))
		require.Equal(t, commonpb.ErrorCode_NO_PERMISSION, retInfo.GetCode())
	}
	checkPermission("GetExecutionCapabilities", []byte(`{}`), false)
	body, err := json.Marshal(map[string]string{"logical_account_id": string(read("logical-id"))})
	require.NoError(t, err)
	checkPermission("GetLogicalAccount", body, true)
	write("gateway-done", []byte("passed"))
	t.Log("production native router -> Trade: method ACL, target signature, durable nonce replay and TLS host identity passed")
}
