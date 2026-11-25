//go:build linux && hostgateway_e2e

package gatewaycontrol

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const primaryPath = "trpc.moox.storage.PrimaryStore"
const collectPath = "trpc.moox.collector.CollectMgr"

// This gate runs production gateway executables against the real Admin service,
// SQLite topology/public-key store and deployment PKI. 调用方用所在主机的测试密钥签名：control 上是 collector，
// storage 上是 monitor。 Separate Linux
// loopback IPs preserve the canonical remote port without endpoint overrides.
// 快照只带主机、部署与公钥，路由、调用名单由网关进程按内嵌目录自己算。客户端的目标服务在本机时直连服务端口
// （不签名、不经本机网关），跨主机才经对端网关的 11003 验签；本机网关的回环端口仍然验签转发，这里用原始请求覆盖它。
// Both pure Go executables may be cross-compiled on the developer's machine.
// closeE2EClient 关掉测试里的网关客户端。场景里网关进程重启过，客户端池里留着的旧 TLS 连接对端已经不在，
// 关闭时发 close_notify 会报 broken pipe 或连接被重置：连接本来就要关，这类错误不算失败。
func closeE2EClient(t *testing.T, c *gatewayclient.Client) {
	t.Helper()
	if err := c.Close(); err != nil && !peerAlreadyGone(err) {
		t.Errorf("关闭网关客户端失败：%v", err)
	}
}

func peerAlreadyGone(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) ||
		strings.Contains(err.Error(), "failed to send closeNotify alert") || strings.Contains(err.Error(), "broken pipe")
}

func TestHostGatewayRealAdminE2E(t *testing.T) {
	binary := requiredE2EFile(t, "MOOX_HOST_GATEWAY_E2E_BINARY")
	framework := requiredE2EFile(t, "MOOX_HOST_GATEWAY_E2E_TRPC_CONFIG")
	f := newControlFixture(t)
	f.service.now = time.Now
	ctx := context.Background()
	// 控制机登记成外部入口：它的网关多开 11004，接外部身份的请求。外部身份能调的方法都在存储机上，
	// 入口放在控制机，外部请求都要由控制机网关带来源头转到存储机，跨主机转发因此有真实进程覆盖。
	controlSpec := sysdeploy.HostSpec{HostID: "control", Enabled: true, Address: "127.0.0.1", ExternalEntry: true, Components: []string{"admin", "collector"}}
	storageSpec := sysdeploy.HostSpec{HostID: "storage", Enabled: true, Address: "127.0.0.2", Components: []string{"storage-primary", "storage-view", "monitor"}}
	f.specs = []sysdeploy.HostSpec{controlSpec, storageSpec}
	require.NoError(t, f.declare())
	for _, identity := range []string{"scf-collector", "moox-skill"} {
		require.NoError(t, f.keys.SetIdentity(ctx, identity, publicKey("identity/"+identity)))
	}

	listened, err := net.Listen("tcp", "127.0.0.1:11112")
	require.NoError(t, err)
	opened := &trackedListener{Listener: listened}
	controlService := server.New(server.WithServiceName(servicecatalog.GatewayControlPath), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithListener(opened), server.WithAddress(opened.Addr().String()), ServerOption())
	require.NoError(t, Register(controlService, f.service))
	go func() { _ = controlService.Serve() }()
	t.Cleanup(func() { _ = controlService.Close(nil) })
	var backendCalls atomic.Int64
	startEchoBackend(t, "127.0.0.1:20102", primaryPath, &backendCalls)

	root := t.TempDir()
	ca, err := pki.Open(filepath.Join(root, "pki"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ca.Close()) })
	_, err = ca.EnsureCA()
	require.NoError(t, err)
	configs := map[string]hostgatewayconfig.Config{}
	for _, host := range []struct{ id, ip string }{{"control", "127.0.0.1"}, {"storage", "127.0.0.2"}} {
		issued, err := ca.Issue(pki.HostIdentity{HostID: host.id, Address: host.ip}, filepath.Join(root, host.id, "identity"))
		require.NoError(t, err)
		cfg := hostgatewayconfig.Default(host.id, "control", "127.0.0.1")
		cfg.Server = hostgatewayconfig.Server{LocalAddr: net.JoinHostPort(host.ip, "11002"), RemoteAddr: net.JoinHostPort(host.ip, "11003"), HealthAddr: net.JoinHostPort(host.ip, "11012")}
		if host.id == "control" {
			cfg.Server.ExternalAddr = net.JoinHostPort(host.ip, "11004")
		}
		cfg.TLS = hostgatewayconfig.TLS{CertificateFile: issued.Certificate, KeyFile: issued.Key, CAFile: issued.CA}
		cfg.Store.Path, cfg.Control.KeyFile = filepath.Join(root, host.id, "cache"), filepath.Join(root, host.id, "host.key")
		require.NoError(t, os.WriteFile(cfg.Control.KeyFile, []byte(gatewayauth.EncodeSigningKey(authtest.Key("host/"+host.id))+"\n"), 0o600))
		configs[host.id] = cfg
	}
	controlProcess := startGatewayExecutable(t, binary, framework, configs["control"])
	storageProcess := startGatewayExecutable(t, binary, framework, configs["storage"])
	waitForE2E(t, 15*time.Second, "both actual gateway heartbeats", func() bool {
		for _, host := range []string{"control", "storage"} {
			status, err := f.topology.GetGatewayStatus(ctx, host)
			if err != nil || status.LastSeenAt == nil || status.AppliedHash != status.ExpectedHash {
				return false
			}
		}
		return true
	})

	collector, console := authtest.Component("collector", "control"), authtest.Component("console", "control")
	local := map[string]gatewayauth.Credentials{"control": collector, "storage": authtest.Component("monitor", "storage")}
	clients := map[string]*gatewayclient.Client{}
	for _, host := range []string{"control", "storage"} {
		cfg := configs[host]
		c, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: local[host], LocalHostID: host, LocalAddress: cfg.Server.LocalAddr, CAFile: cfg.TLS.CAFile, Serialization: codec.SerializationTypePB, Timeout: 2 * time.Second})
		require.NoError(t, err)
		t.Cleanup(func() { closeE2EClient(t, c) })
		clients[host] = c
		// PrimaryStore 在 storage 上：control 的客户端跨主机经 storage 网关的 11003 验签转发；
		// storage 的客户端直连本机服务端口，不经网关。两条路都到同一个假服务。
		require.Equal(t, []string{"storage"}, c.Directory().Services[primaryPath])
		reached := backendCalls.Load()
		var result wrapperspb.StringValue
		require.NoError(t, c.Invoke(ctx, primaryPath, "ReadTimeSeriesRows", wrapperspb.String("object through "+host), &result))
		require.Equal(t, "object through "+host, result.Value)
		require.Equal(t, reached+1, backendCalls.Load())
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
		// 管理台只在部署了 admin 的控制机上。
		if host != "control" {
			continue
		}
		browser, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: console, LocalHostID: host, LocalAddress: cfg.Server.LocalAddr, CAFile: cfg.TLS.CAFile, Serialization: codec.SerializationTypeJSON, Timeout: 2 * time.Second})
		require.NoError(t, err)
		raw := []byte(" { \"fixture\" : true }\n")
		resultJSON, err := browser.Forward(ctx, primaryPath, "UpsertFields", codec.SerializationTypeJSON, raw)
		require.NoError(t, err)
		require.Equal(t, raw, resultJSON)
		require.NoError(t, browser.Close())
	}
	// 同机直连不查方法名单：storage 上的 host-agent 不在 PrimaryStore 任何方法的调用名单里，直连本机服务端口照样能调；
	// 同一个请求签了名送到本机网关的回环端口，网关按名单拒绝。
	require.False(t, mustE2ECatalog(t).Allowed("host-agent", primaryPath, "ReadTimeSeriesRows"))
	agent := authtest.Component("host-agent", "storage")
	direct, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: agent, LocalHostID: "storage", LocalAddress: configs["storage"].Server.LocalAddr, CAFile: configs["storage"].TLS.CAFile, Serialization: codec.SerializationTypePB, Timeout: 2 * time.Second})
	require.NoError(t, err)
	body := []byte{0x0a, 1, 'x'}
	reached := backendCalls.Load()
	echoed, err := direct.Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
	require.NoError(t, err)
	require.Equal(t, body, echoed)
	require.Equal(t, reached+1, backendCalls.Load())
	require.NoError(t, direct.Close())
	_, err = e2eRawCall(configs["storage"].Server.LocalAddr, nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, agent, "storage", primaryPath, "ReadTimeSeriesRows", body))
	require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "本机网关仍按调用名单拒绝")
	// 本机网关的回环端口仍然验签转发：monitor 用 storage 的主机密钥签名。
	echoed, err = e2eRawCall(configs["storage"].Server.LocalAddr, nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, local["storage"], "storage", primaryPath, "ReadTimeSeriesRows", body))
	require.NoError(t, err)
	require.Equal(t, body, echoed)
	require.Equal(t, reached+2, backendCalls.Load())
	t.Log("SCENARIO PASS object-and-exact-raw-pb-json-local-and-tls")

	storageTLS := e2eTrust(t, configs["storage"].TLS.CAFile, "storage")
	headers := signedE2E(t, collector, "storage", primaryPath, "ReadTimeSeriesRows", body)
	_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, storageTLS, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, headers)
	require.NoError(t, err)
	_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, storageTLS, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, headers)
	require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "同一个签名第二次出现")
	_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, collector, "storage", primaryPath, "ReadTimeSeriesRows", body))
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
	_, err = e2eRawCall(configs["control"].Server.LocalAddr, nil, "trpc.moox.admin.ActivityLog", "AppendActivityLogs", codec.SerializationTypePB, nil, signedE2E(t, console, "control", "trpc.moox.admin.ActivityLog", "AppendActivityLogs", nil))
	require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "console must be denied at the gateway itself")
	// 别的主机的密钥不能冒充：用存储机的密钥自称控制机上的 collector，或自称部署在存储机上的 collector，都被拒。
	for _, forged := range []gatewayauth.Credentials{
		{Caller: "collector", Host: "control", Key: authtest.Key("host/storage")},
		{Caller: "collector", Host: "storage", Key: authtest.Key("host/storage")},
	} {
		_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, storageTLS, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, forged, "storage", primaryPath, "ReadTimeSeriesRows", body))
		require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "forged %v", forged)
	}
	t.Log("SCENARIO PASS tls-trust-plaintext-acl-and-durable-replay")

	// 外部入口（控制机的 11004，明文加签名）：外部身份签名的目标主机是入口主机。
	entry := configs["control"].Server.ExternalAddr
	scf, skill := authtest.Identity("scf-collector"), authtest.Identity("moox-skill")
	external := func(credential gatewayauth.Credentials, service, method string) ([]byte, error) {
		return e2eRawCall(entry, nil, service, method, codec.SerializationTypePB, body, signedE2E(t, credential, "control", service, method, body))
	}
	// 正向：目标在别的主机，控制机网关带来源头转到存储机（scf-collector 写行情、moox-skill 读行情）。
	reached = backendCalls.Load()
	for _, permitted := range []struct {
		credential gatewayauth.Credentials
		method     string
	}{{scf, "CommitTimeSeriesBatch"}, {scf, "UpsertFields"}, {skill, "ReadTimeSeriesRows"}} {
		echoed, err = external(permitted.credential, primaryPath, permitted.method)
		require.NoError(t, err, "%s 经控制机入口调存储机的 %s", permitted.credential.Caller, permitted.method)
		require.Equal(t, body, echoed)
	}
	require.Equal(t, reached+3, backendCalls.Load())
	// 名单外的方法、不是外部身份的调用方、重放：都在入口被拒，到不了服务。
	for _, denied := range []struct {
		credential      gatewayauth.Credentials
		service, method string
	}{
		{skill, primaryPath, "UpsertFields"}, {scf, primaryPath, "ReadTimeSeriesRows"}, {scf, collectPath, "SetSubjectExclusions"},
		{collector, primaryPath, "ReadTimeSeriesRows"}, {authtest.Identity("moox-cli"), primaryPath, "ReadTimeSeriesRows"},
	} {
		_, err = external(denied.credential, denied.service, denied.method)
		require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "%s 经外部入口调 %s/%s", denied.credential.Caller, denied.service, denied.method)
	}
	replayed := signedE2E(t, skill, "control", primaryPath, "ReadTimeSeriesRows", body)
	_, err = e2eRawCall(entry, nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, replayed)
	require.NoError(t, err)
	_, err = e2eRawCall(entry, nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, replayed)
	require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "同一个签名第二次出现在外部入口")
	// 外部身份的签名拿到 11003 上直接用不行；存储机没开外部入口，11004 上没有监听。
	_, err = e2eRawCall(configs["storage"].Server.RemoteAddr, storageTLS, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, skill, "storage", primaryPath, "ReadTimeSeriesRows", body))
	require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "外部身份不走 11003")
	_, err = e2eRawCall("127.0.0.2:11004", nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, skill, "storage", primaryPath, "ReadTimeSeriesRows", body))
	require.Error(t, err, "存储机不开外部入口")
	// 伪造来源头（X-Moox-Origin-Caller）：三条检查缺一条就拒。
	forwarded := func(credential gatewayauth.Credentials, origin, method string) error {
		headers := signedE2E(t, credential, "storage", primaryPath, method, body)
		headers.Set(gatewayauth.HeaderOriginCaller, origin)
		_, err := e2eRawCall(configs["storage"].Server.RemoteAddr, storageTLS, primaryPath, method, codec.SerializationTypePB, body, headers)
		return err
	}
	storageGateway, controlGateway := authtest.Component("host-gateway", "storage"), authtest.Component("host-gateway", "control")
	reached = backendCalls.Load()
	for name, forgedErr := range map[string]error{
		"存储机不是外部入口，它的 host-gateway 带来源头不认":      forwarded(storageGateway, "scf-collector", "CommitTimeSeriesBatch"),
		"带来源头的不是 host-gateway（控制机上的 collector）": forwarded(collector, "scf-collector", "CommitTimeSeriesBatch"),
		"入口主机的 host-gateway 冒充 moox-cli":        forwarded(controlGateway, "moox-cli", "ReadTimeSeriesRows"),
		"入口主机的 host-gateway 冒充 console":         forwarded(controlGateway, "console", "ReadTimeSeriesRows"),
		"入口主机的 host-gateway 冒充组件":               forwarded(controlGateway, "collector", "ReadTimeSeriesRows"),
		"外部身份不在这个方法的名单上":                        forwarded(controlGateway, "moox-skill", "CommitTimeSeriesBatch"),
		"不带来源头的 host-gateway 调业务方法": func() error {
			_, err := e2eRawCall(configs["storage"].Server.RemoteAddr, storageTLS, primaryPath, "CommitTimeSeriesBatch", codec.SerializationTypePB, body, signedE2E(t, controlGateway, "storage", primaryPath, "CommitTimeSeriesBatch", body))
			return err
		}(),
	} {
		require.EqualValues(t, gatewayauth.RetRejected, errs.Code(forgedErr), name)
	}
	require.Equal(t, reached, backendCalls.Load(), "伪造来源头的请求没有到达 Storage")
	// 对照：入口主机的 host-gateway 带名单上的外部身份，目标网关放行。
	require.NoError(t, forwarded(controlGateway, "scf-collector", "CommitTimeSeriesBatch"))
	require.Equal(t, reached+1, backendCalls.Load())
	t.Log("SCENARIO PASS external-entry-cross-host-forward-acl-and-origin-caller-rules")

	// A newly registered host is also a new caller. Its signature must become
	// accepted through the already running control gateway after the next pull.
	require.NoError(t, f.declareHost(sysdeploy.HostSpec{HostID: "joining", Address: "127.0.0.3"}))
	require.NoError(t, f.keys.SetHostPublicKey(ctx, "joining", publicKey("host/joining")))
	joining := authtest.Component("host-gateway", "joining")
	joiningBody, err := codec.Marshal(codec.SerializationTypePB, &pb.PullSnapshotReq{HostId: "joining"})
	require.NoError(t, err)
	waitForE2E(t, 15*time.Second, "new caller accepted without gateway restart", func() bool {
		raw, err := e2eRawCall(configs["control"].Server.RemoteAddr, e2eTrust(t, configs["control"].TLS.CAFile, "control"), servicecatalog.GatewayControlPath, "PullSnapshot", codec.SerializationTypePB, joiningBody, signedE2E(t, joining, "control", servicecatalog.GatewayControlPath, "PullSnapshot", joiningBody))
		if err != nil {
			return false
		}
		var rsp pb.PullSnapshotRsp
		return codec.Unmarshal(codec.SerializationTypePB, raw, &rsp) == nil && rsp.Changed && snapshotHost(rsp.GetSnapshot(), "joining") != nil
	})
	t.Log("SCENARIO PASS new-caller-next-snapshot-without-restart")

	f.specs[1].DisabledComponents = []string{"storage-primary"}
	require.NoError(t, f.declare())
	waitForE2E(t, 15*time.Second, "disabled deployment explicitly rejected", func() bool {
		_, err := clients["control"].Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
		return err != nil && (strings.Contains(err.Error(), "no enabled deployment") || strings.Contains(err.Error(), "not deployed"))
	})
	f.specs[0] = sysdeploy.HostSpec{HostID: "control", Enabled: true, Address: "127.0.0.1", ExternalEntry: true, Components: append(slices.Clone(controlSpec.Components), "storage-primary")}
	f.specs[1] = sysdeploy.HostSpec{HostID: "storage", Enabled: true, Address: "127.0.0.2", Components: []string{"storage-view", "monitor"}}
	require.NoError(t, f.declare())
	// PrimaryStore 换到 control：control 的客户端改成直连本机服务端口，storage 的客户端改成跨主机经 control 网关的 11003。
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
		_, err := e2eRawCall(configs["storage"].Server.LocalAddr, nil, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, collector, "storage", primaryPath, "ReadTimeSeriesRows", body))
		return err != nil && strings.Contains(err.Error(), "not deployed")
	})
	t.Log("SCENARIO PASS deployment-withdrawal-and-automatic-target-switch")

	storageProcess.stop(t)
	storageProcess = startGatewayExecutable(t, binary, framework, configs["storage"])
	restartedAt := time.Now().UTC()
	waitForE2E(t, 15*time.Second, "restarted process pulls again", func() bool {
		status := f.status(t)
		return status.LastSeenAt != nil && status.LastSeenAt.After(restartedAt) && status.AppliedHash == status.ExpectedHash
	})
	t.Log("SCENARIO PASS normal-restart")

	// Keep control's business route active, but stop the actual Admin control
	// listener. Both gateways retain their previously authenticated snapshots.
	// storage 的客户端跨主机经 control 网关调到 PrimaryStore：验签与路由都靠 control 网关手里的快照。
	// tRPC 服务关闭只关监听，已建立的连接还会再处理一个请求才断：网关连接池里留着的连接会让关闭后的下一次拉取照样成功，
	// 最近一次成功同步就晚于这里，94 秒等不到降级。一并断开已接受的连接，才和 Admin 进程退出一样，从此刻起拉取全部失败。
	require.NoError(t, controlService.Close(nil))
	opened.closeAccepted()
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
	// Admin 仍然不可用：重启后的 control 网关从落盘的快照重新算出路由与调用名单。
	// 新建的本机客户端读到它缓存的目录，目标在本机，直连服务端口。
	restarted, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: collector, LocalHostID: "control", LocalAddress: configs["control"].Server.LocalAddr, CAFile: configs["control"].TLS.CAFile, Serialization: codec.SerializationTypePB, Timeout: 2 * time.Second})
	require.NoError(t, err)
	defer restarted.Close()
	require.Equal(t, []string{"control"}, restarted.Directory().Services[primaryPath])
	result, err = restarted.Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
	require.NoError(t, err)
	require.Equal(t, body, result)
	// 跨主机的调用要经重启后的 control 网关验签转发：storage 的客户端（旧连接已断，重连）与一条原始签名请求都要通过，
	// 不在名单里的调用方照样被拒。
	before = backendCalls.Load()
	waitForE2E(t, 10*time.Second, "existing client reconnects to the restarted control gateway", func() bool {
		result, err := clients["storage"].Forward(ctx, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body)
		return err == nil && slices.Equal(result, body)
	})
	controlTLS := e2eTrust(t, configs["control"].TLS.CAFile, "control")
	result, err = e2eRawCall(configs["control"].Server.RemoteAddr, controlTLS, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, local["storage"], "control", primaryPath, "ReadTimeSeriesRows", body))
	require.NoError(t, err)
	require.Equal(t, body, result)
	require.GreaterOrEqual(t, backendCalls.Load(), before+2)
	_, err = e2eRawCall(configs["control"].Server.RemoteAddr, controlTLS, primaryPath, "ReadTimeSeriesRows", codec.SerializationTypePB, body, signedE2E(t, agent, "control", primaryPath, "ReadTimeSeriesRows", body))
	require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "重启后的网关仍按调用名单拒绝")
	t.Log("SCENARIO PASS offline-routing-90-second-readiness-and-cache-restart")
}

func mustE2ECatalog(t *testing.T) servicecatalog.Catalog {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	return catalog
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
	cmd.Env = append(os.Environ(), "MOOX_INSTANCE_ID="+cfg.Host.ID, "MOOX_NODE_ID="+cfg.Host.ID, "MOOX_BOOT_ID=synthetic-boot", "MOOX_METRICS_EVENTBUS_URL=nats://127.0.0.1:1", "MOOX_GATEWAY_ROUTE_SYNC_STALE_AFTER_SECONDS=90")
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

// trackedListener 记下 Admin 监听接受过的连接，停 Admin 时由 closeAccepted 一并断开。
type trackedListener struct {
	net.Listener
	mu       sync.Mutex
	accepted []net.Conn
}

func (l *trackedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.accepted = append(l.accepted, c)
		l.mu.Unlock()
	}
	return c, err
}

func (l *trackedListener) closeAccepted() {
	l.mu.Lock()
	accepted := l.accepted
	l.accepted = nil
	l.mu.Unlock()
	for _, c := range accepted {
		_ = c.Close()
	}
}

func startEchoBackend(t *testing.T, address, path string, calls *atomic.Int64) {
	t.Helper()
	l, err := net.Listen("tcp", address)
	require.NoError(t, err)
	svc := server.New(server.WithServiceName(path), server.WithListener(l), server.WithAddress(l.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTransport(transport.NewServerTransport()), server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	require.NoError(t, svc.Register(&server.ServiceDesc{ServiceName: path, HandlerType: (*interface{})(nil), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
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
	headers, err := gatewayauth.Sign(credential, gatewayauth.Request{TargetNode: host, Callee: service, Func: method, Body: body}, time.Now())
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
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/readyz", nil)
	require.NoError(t, err)
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		return 0
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode
}
