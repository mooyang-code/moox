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
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	directorypb "github.com/mooyang-code/moox/packages/gatewayclient/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"math/big"
	"net"
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
	keyPath := filepath.Join(root, "secrets", "host.key")
	caPath := filepath.Join(root, "pki", "ca.crt")
	for _, path := range []string{configPath, keyPath, caPath} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	}
	credentials := authtest.Component("trade", "control")
	require.NoError(t, os.WriteFile(keyPath, []byte(gatewayauth.EncodeSigningKey(credentials.Key)+"\n"), 0600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// Trade 与 Admin 同机：直连服务目录里 SecretMgr 的回环端口，这里指到假服务上。
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	servicePorts := map[int]bool{}
	for _, path := range []string{"trpc.moox.ops.SecretMgr"} {
		spec, ok := catalog.Service(path)
		require.True(t, ok, path)
		servicePorts[spec.Port] = true
	}
	t.Cleanup(gatewayclient.SetLocalServiceAddressForTest(func(port int) string {
		if !servicePorts[port] {
			return "127.0.0.1:1"
		}
		return listener.Addr().String()
	}))
	const directoryVersion = "v1"

	var mu sync.Mutex
	secretCalls := 0
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
				rsp := &directorypb.GetDirectoryRsp{Version: directoryVersion}
				if req.GetCurrentVersion() != directoryVersion {
					rsp.Changed = true
					rsp.Hosts = []*directorypb.DirectoryHost{{HostId: "control", Address: "control.example.test", Reachability: "direct", Enabled: true}}
					rsp.Deployments = []*directorypb.DirectoryDeployment{{HostId: "control", ComponentId: "admin", Enabled: true}, {HostId: "control", ComponentId: "trade", Enabled: true}}
				}
				encoded, err := proto.Marshal(rsp)
				return &codec.Body{Data: encoded}, err
			}
			if message.ServerRPCName() != "/trpc.moox.ops.SecretMgr/GetSecretValue" || message.SerializationType() != codec.SerializationTypeJSON {
				return nil, errors.New("unexpected Trade secret call")
			}
			// 同机直连的请求不带任何签名头。
			for name := range message.ServerMetaData() {
				if strings.HasPrefix(strings.ToLower(name), "x-moox-") {
					return nil, errors.New("direct local call carries signature header " + name)
				}
			}
			var req struct {
				SecretID string `json:"secret_id"`
			}
			if err := json.Unmarshal(body, &req); err != nil || req.SecretID != "secret-1" {
				return nil, errors.New("secret identity changed")
			}
			mu.Lock()
			defer mu.Unlock()
			secretCalls++
			encoded, err := json.Marshal(map[string]any{"ret_info": map[string]any{"code": 0}, "secret": map[string]any{"secret_id": req.SecretID, "category": "exchange", "pro" + "vider": "binance", "status": "active", "key_id": "exchange-key", "secret_value": "exchange-secret"}})
			return &codec.Body{Data: encoded}, err
		})
	}}}}
	require.NoError(t, svc.Register(descriptor, struct{}{}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	data := filepath.Join(root, "data", "trade")
	raw := "database:\n  path: " + filepath.Join(data, "trade.db") + "\ngateway_client:\n  caller: trade\n  key_file: ../../secrets/host.key\n  host_id: control\n  local_address: " + listener.Addr().String() + "\n  ca_file: " + caPath + "\n"
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
	require.Equal(t, 2, secretCalls)
	mu.Unlock()
	require.NoError(t, gateway.Close())
	_, err = client.GetExchangeSecret(t.Context(), "secret-1")
	require.Error(t, err)
}
