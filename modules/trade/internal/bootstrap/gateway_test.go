package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/secretclient"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestTradeSecretsUseProcessIdentityOverNativeRPC(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "trade", "config", "app.yaml")
	hostPath := filepath.Join(root, "hostgateway", "config", "app.yaml")
	keyPath := filepath.Join(root, "secrets", "caller-trade.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, hostPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	}
	credentials := gatewayauth.Credentials{Caller: "trade", KeyID: "admin-assigned-trade-key-42", Secret: "trade-fixture-signing-key-at-least-32-bytes"}
	require.NoError(t, os.WriteFile(keyPath, []byte(credentials.Secret+"\n"), 0600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host := hostgatewayconfig.Default("control", "control", "192.0.2.1", "host-fixture-key")
	host.Server.LocalAddr = listener.Addr().String()
	host.TLS.CAFile = caPath
	encoded, err := hostgatewayconfig.Encode(host)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostPath, encoded, 0600))
	directory := servicecatalog.Directory{Services: map[string][]string{"trpc.moox.ops.SecretMgr": {"control"}}, Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "control.example.test"}}}
	directory.Version, err = directory.VersionHash()
	require.NoError(t, err)

	var mu sync.Mutex
	nonces := map[string]bool{}
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	descriptor := &server.ServiceDesc{ServiceName: "trpc.moox.hostgateway.ServiceGateway", HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ any, ctx context.Context, decode server.FilterFunc) (any, error) {
		raw := &codec.Body{}
		filters, err := decode(raw)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, raw, func(ctx context.Context, input any) (any, error) {
			message := codec.Message(ctx)
			body := input.(*codec.Body).Data
			if strings.HasSuffix(message.ServerRPCName(), "/GetDirectory") {
				var req directorypb.GetDirectoryReq
				if err := proto.Unmarshal(body, &req); err != nil {
					return nil, err
				}
				rsp := &directorypb.GetDirectoryRsp{Version: directory.Version}
				if req.GetCurrentVersion() != directory.Version {
					rsp.Changed = true
					rsp.Services = map[string]*directorypb.ServiceHosts{"trpc.moox.ops.SecretMgr": {HostIds: []string{"control"}}}
					rsp.Hosts = map[string]*directorypb.DirectoryHost{"control": {Address: "control.example.test"}}
				}
				encoded, err := proto.Marshal(rsp)
				return &codec.Body{Data: encoded}, err
			}
			if message.ServerRPCName() != "/trpc.moox.ops.SecretMgr/GetSecretValue" || message.SerializationType() != codec.SerializationTypeJSON {
				return nil, errors.New("unexpected Trade secret call")
			}
			headers := http.Header{}
			for name, value := range message.ServerMetaData() {
				headers.Set(name, string(value))
			}
			claims, err := gatewayauth.Verify(credentials, gatewayauth.Request{Method: "POST", Path: message.ServerRPCName(), TargetNode: "control", Callee: "trpc.moox.ops.SecretMgr", Func: "GetSecretValue", Body: body}, headers, time.Now())
			if err != nil {
				return nil, err
			}
			var req struct {
				SecretID string `json:"secret_id"`
			}
			if err := json.Unmarshal(body, &req); err != nil || req.SecretID != "secret-1" {
				return nil, errors.New("secret identity changed")
			}
			mu.Lock()
			defer mu.Unlock()
			if nonces[claims.Nonce] {
				return nil, errors.New("nonce reused")
			}
			nonces[claims.Nonce] = true
			encoded, err := json.Marshal(map[string]any{"ret_info": map[string]any{"code": 0}, "secret": map[string]any{"secret_id": req.SecretID, "category": "exchange", "pro" + "vider": "binance", "status": "active", "key_id": "exchange-key", "secret_value": "exchange-secret"}})
			return &codec.Body{Data: encoded}, err
		})
	}}}}
	require.NoError(t, svc.Register(descriptor, struct{}{}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	data := filepath.Join(root, "data", "trade")
	raw := "database:\n  path: " + filepath.Join(data, "trade.db") + "\ngateway_client:\n  caller: trade\n  key_id: " + credentials.KeyID + "\n  key_file: ../../secrets/caller-trade.key\n"
	require.NoError(t, os.WriteFile(configPath, []byte(raw), 0600))
	cfg, err := config.Load(configPath)
	require.NoError(t, err)
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	client := secretclient.New(gateway)
	for range 2 {
		secret, err := client.GetExchangeSecret(t.Context(), "secret-1")
		require.NoError(t, err)
		require.Equal(t, "exchange-key", secret.KeyID)
	}
	mu.Lock()
	require.Len(t, nonces, 2)
	mu.Unlock()
	require.NoError(t, gateway.Close())
	_, err = client.GetExchangeSecret(t.Context(), "secret-1")
	require.Error(t, err)
}
