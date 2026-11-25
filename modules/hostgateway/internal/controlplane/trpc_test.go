package controlplane

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testcert"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestSignedTLSControlProtocolAndProcessIdentity(t *testing.T) {
	for _, hostID := range []string{"control", "storage"} {
		t.Run(hostID, func(t *testing.T) {
			ca := testcert.New(t, nil)
			cfg := hostgatewayconfig.Default(hostID, "control", "127.0.0.1")
			cfg.TLS = testcert.Files(t, ca, hostID, nil)
			material, err := tlsconfig.Load(hostID, cfg.TLS)
			require.NoError(t, err)
			address := "127.0.0.1:11112"
			if hostID != "control" {
				address = testrpc.Address(t)
			}
			opened, err := net.Listen("tcp", address)
			require.NoError(t, err)
			if hostID != "control" {
				serverMaterial, err := tlsconfig.Load("control", testcert.Files(t, ca, "control", nil))
				require.NoError(t, err)
				opened = tls.NewListener(opened, serverMaterial.Server())
			}
			cfg.Control.Target = address
			credential := testsnapshot.Credential("host-gateway", hostID)
			raw := testsnapshot.New(t, hostID)
			var mu sync.Mutex
			seen := map[string]bool{}
			testrpc.Serve(t, opened, servicecatalog.GatewayControlPath, func(ctx context.Context, body []byte) ([]byte, error) {
				message := codec.Message(ctx)
				method := strings.TrimPrefix(message.ServerRPCName(), "/"+servicecatalog.GatewayControlPath+"/")
				headers := http.Header{}
				for k, v := range message.ServerMetaData() {
					headers.Add(k, string(v))
				}
				if hostID == "control" {
					// 控制机网关直连本机的 Admin：不签名，自己带上签名主机头。
					require.Equal(t, "control", headers.Get(gatewayauth.HeaderVerifiedHost))
					require.Empty(t, headers.Get("X-Moox-Signature"))
				} else {
					// 其余主机发给控制机网关（这里的夹具）：用本机的主机密钥、以 host-gateway 的身份签名，每次一个新 nonce。
					claims, err := authtest.Verify(credential, gatewayauth.Request{TargetNode: "control", Callee: servicecatalog.GatewayControlPath, Func: method, Body: body}, headers, time.Now())
					if err != nil {
						return nil, err
					}
					require.Empty(t, headers.Get(gatewayauth.HeaderVerifiedHost), "签名主机头只能由验签的网关写")
					mu.Lock()
					duplicate := seen[claims.Nonce]
					seen[claims.Nonce] = true
					mu.Unlock()
					require.False(t, duplicate)
				}
				require.Equal(t, "PullSnapshot", method)
				req := &pb.PullSnapshotReq{}
				require.NoError(t, proto.Unmarshal(body, req))
				require.Equal(t, hostID, req.HostId)
				rsp := &pb.PullSnapshotRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}
				if req.CurrentHash != raw.Hash {
					rsp.Changed, rsp.Snapshot = true, raw
				}
				return proto.Marshal(rsp)
			})
			c, err := NewRPC(cfg, material, credential)
			require.NoError(t, err)
			defer c.Close()
			view, err := c.Pull(context.Background(), "")
			require.NoError(t, err)
			require.Equal(t, raw.Hash, view.Hash())
			unchanged, err := c.Pull(context.Background(), view.Hash())
			require.NoError(t, err)
			require.Nil(t, unchanged)
			require.NoError(t, c.Close())
			_, err = c.Pull(context.Background(), "")
			require.Error(t, err)
		})
	}
}
