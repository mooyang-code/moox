package view

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type metadataGatewayDirectory struct{ directory servicecatalog.Directory }

func (s metadataGatewayDirectory) Fetch(_ context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	return gatewayclient.DirectoryUpdate{Changed: version != s.directory.Version, Directory: s.directory.Clone()}, nil
}

type metadataGatewayWire struct {
	pb.UnimplementedMetadata
	credentials gatewayauth.Credentials
	mu          sync.Mutex
	nonces      map[string]bool
	reads       int
	writes      int
}

func (s *metadataGatewayWire) verify(ctx context.Context, method string, request proto.Message) error {
	headers := http.Header{}
	for key, value := range codec.Message(ctx).ServerMetaData() {
		headers.Set(key, string(value))
	}
	body, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	claims, err := gatewayauth.Verify(s.credentials, gatewayauth.Request{Method: "POST", Path: "/trpc.moox.storage.Metadata/" + method,
		TargetNode: "storage", Callee: "trpc.moox.storage.Metadata", Func: method, Body: body}, headers, time.Now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonces[claims.Nonce] {
		return errors.New("Metadata call reused a signing nonce")
	}
	s.nonces[claims.Nonce] = true
	return nil
}

func (s *metadataGatewayWire) ListViews(ctx context.Context, req *pb.ListViewsReq) (*pb.ListViewsRsp, error) {
	if err := s.verify(ctx, "ListViews", req); err != nil {
		return nil, err
	}
	if req.GetAuthInfo().GetAppId() != "storage-view" || req.GetAuthInfo().GetAppKey() != "storage-internal-role-auth" || req.GetSpaceId() != "crypto" {
		return nil, errors.New("Storage role authentication or request body changed")
	}
	s.mu.Lock()
	s.reads++
	reads := s.reads
	s.mu.Unlock()
	if reads == 1 {
		return nil, errs.NewFrameError(errs.RetServerNoService, "refresh and retry this read")
	}
	return &pb.ListViewsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Views: []*pb.View{{SpaceId: "crypto", ViewId: "view-1"}}}, nil
}

func (s *metadataGatewayWire) ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ClaimViewIndexBuildRsp, error) {
	if err := s.verify(ctx, "ClaimViewIndexBuild", req); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	return nil, errs.NewFrameError(errs.RetServerNoService, "write must not retry")
}

func TestMetadataGatewayPreservesRoleAuthAndBoundsRetriesOverTRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	credentials := gatewayauth.Credentials{Caller: "storage-view", KeyID: "storage-view-generated-key-id", Secret: "storage-view-fixture-signing-key-at-least-32-bytes"}
	wire := &metadataGatewayWire{credentials: credentials, nonces: map[string]bool{}}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	pb.RegisterMetadataService(svc, wire)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	directory := servicecatalog.Directory{Services: map[string][]string{"trpc.moox.storage.Metadata": {"storage"}}, Hosts: map[string]servicecatalog.DirectoryHost{"storage": {Address: "storage.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	gateway, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: credentials, LocalHostID: "storage", LocalAddress: listener.Addr().String(), CAFile: caFile, Source: metadataGatewayDirectory{directory}, RefreshInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	metadata := NewGatewayMetadataClient(gateway)
	rsp, err := metadata.ListViews(t.Context(), &pb.ListViewsReq{AuthInfo: &pb.AuthInfo{AppId: "storage-view", AppKey: "storage-internal-role-auth"}, SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, "view-1", rsp.GetViews()[0].GetViewId())
	_, err = metadata.ClaimViewIndexBuild(t.Context(), &pb.ClaimViewIndexBuildReq{})
	require.Error(t, err)
	require.Equal(t, int(errs.RetServerNoService), int(errs.Code(err)))
	wire.mu.Lock()
	require.Equal(t, 2, wire.reads)
	require.Equal(t, 1, wire.writes)
	require.Len(t, wire.nonces, 3)
	wire.mu.Unlock()
	_, err = metadata.ListViews(t.Context(), &pb.ListViewsReq{}, client.WithTarget("ip://192.0.2.1:20200"))
	require.ErrorContains(t, err, "instead of tRPC client options")
}
