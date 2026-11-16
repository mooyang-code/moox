package gatewayclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

// secretService 属于 admin 组件；monitorService 属于 monitor 组件，用来放在控制机以外的主机上。
const (
	secretService  = "trpc.moox.ops.SecretMgr"
	monitorService = "trpc.moox.monitor.MonitorMgr"
)

type sourceStub struct {
	mu        sync.Mutex
	directory DirectoryUpdate
	err       error
	calls     int
}

func (s *sourceStub) Fetch(_ context.Context, version string) (DirectoryUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return DirectoryUpdate{}, s.err
	}
	update := s.directory
	update.Changed = version != update.Version
	update.Hosts, update.Deployments = slices.Clone(update.Hosts), slices.Clone(update.Deployments)
	return update, nil
}

type tunnelFunc func(context.Context, string) (string, error)

func (f tunnelFunc) Resolve(ctx context.Context, id string) (string, error) { return f(ctx, id) }

// testDirectory 是下发的目录：每台主机都启用并部署了 admin（secretService 所在的组件）。
func testDirectory(t *testing.T, hosts ...string) DirectoryUpdate {
	t.Helper()
	update := DirectoryUpdate{}
	for _, host := range hosts {
		update.Hosts = append(update.Hosts, servicecatalog.Host{ID: host, Address: host + ".example.test", Status: servicecatalog.Enabled})
		update.Deployments = append(update.Deployments, servicecatalog.Deployment{HostID: host, ComponentID: "admin", Status: servicecatalog.Enabled})
	}
	return versioned(t, update)
}

// versioned 给目录填上随内容变化的版本号（下发方给的版本只用来判断有没有变化）。
func versioned(t *testing.T, update DirectoryUpdate) DirectoryUpdate {
	t.Helper()
	update.Version = ""
	raw, err := json.Marshal(update)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	update.Version = hex.EncodeToString(sum[:])
	return update
}

// resolved 是客户端按内嵌服务目录展开后应得到的目录。
func resolved(t *testing.T, update DirectoryUpdate) servicecatalog.Directory {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	directory, err := resolve(catalog, update)
	require.NoError(t, err)
	return directory
}

// internalConfig 是主机 local 上的 monitor 组件。
func internalConfig(t *testing.T, source DirectorySource, local string) Config {
	t.Helper()
	ca, _, _ := testCertificates(t)
	return Config{Mode: Internal, Credentials: authtest.Component("monitor", local), LocalHostID: local, CAFile: ca,
		Source: source, RefreshInterval: time.Hour}
}

func signatureHeaders(headers http.Header) []string {
	var names []string
	for name := range headers {
		if strings.HasPrefix(strings.ToLower(name), "x-moox-") {
			names = append(names, name)
		}
	}
	return names
}

func tunnelConfig(source DirectorySource) Config {
	return Config{Mode: Tunnel, Credentials: authtest.Identity("moox-cli"),
		Source: source, RefreshInterval: time.Hour, Tunnels: tunnelFunc(func(context.Context, string) (string, error) { return "127.0.0.1:12345", nil })}
}

func newTestClient(t *testing.T, config Config) *Client {
	t.Helper()
	c, err := New(config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}

// 落盘缓存存的是下发的主机与部署；进程重启、取不到目录时按内嵌的服务目录重新展开。
func TestRefreshCacheFallbackAndRejectsInvalidSnapshot(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "b", "a")}
	config := tunnelConfig(source)
	config.CachePath = filepath.Join(t.TempDir(), "gatewayclient", "directory.json")
	c := newTestClient(t, config)
	cached, err := readCache(config.CachePath)
	require.NoError(t, err)
	require.Equal(t, source.directory, cached, "缓存里是主机加部署，不是展开后的服务表")
	raw, err := os.ReadFile(config.CachePath)
	require.NoError(t, err)
	require.NotContains(t, string(raw), secretService, "缓存不存按服务展开的结果")
	stat, err := os.Stat(config.CachePath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), stat.Mode().Perm())
	want := resolved(t, source.directory)
	require.Equal(t, want, c.Directory())
	require.Equal(t, []string{"a", "b"}, want.Services[secretService])
	require.Equal(t, servicecatalog.DirectoryHost{Address: "a.example.test"}, want.Hosts["a"])
	owned := c.Directory()
	owned.Services[secretService][0] = "modified"
	require.Equal(t, "a", c.Directory().Services[secretService][0])
	// 展开后不合法的目录（主机地址非法）整份拒绝，继续用上一份。
	valid := source.directory
	broken := testDirectory(t, "b", "a")
	broken.Hosts[0].Address = "bad address"
	source.directory = versioned(t, broken)
	require.Error(t, c.Refresh(context.Background()))
	require.Equal(t, want, c.Directory())
	cached, err = readCache(config.CachePath)
	require.NoError(t, err)
	require.Equal(t, valid, cached, "被拒绝的目录不落盘")
	source.err = errors.New("local gateway offline")
	offline := newTestClient(t, config)
	require.Equal(t, want, offline.Directory(), "重启后按内嵌目录把缓存的主机与部署重新展开")
	// 缓存里内嵌目录不认识的组件被忽略，其余照常展开。
	unknown := valid
	unknown.Deployments = append(slices.Clone(valid.Deployments), servicecatalog.Deployment{HostID: "a", ComponentID: "future-component", Status: servicecatalog.Enabled})
	require.NoError(t, writeCache(config.CachePath, unknown))
	upgraded := newTestClient(t, config)
	require.Equal(t, want, upgraded.Directory())
	for _, corrupt := range []string{`{"version":""}`, `{"version":"v1","hosts":[],"deployments":[]}`, `{"version":"v1","hosts":[{"id":"a","address":"bad address","status":"enabled"}],"deployments":[]}`, `{"version":"v1","services":{}}`, `not json`} {
		require.NoError(t, os.WriteFile(config.CachePath, []byte(corrupt), 0o600))
		_, err = New(config)
		require.ErrorContains(t, err, "initial directory unavailable", corrupt)
	}
}

func TestReadRetryMovesHostAndResignsWhileWritesRunOnce(t *testing.T) {
	for _, method := range []string{"GetSecret", "UpdateSecret"} {
		t.Run(method, func(t *testing.T) {
			source := &sourceStub{directory: testDirectory(t, "b", "a")}
			c := newTestClient(t, tunnelConfig(source))
			calls := 0
			var nonce string
			body := []byte(" {\"key\": \"value\"} \n")
			c.invoke = func(_ context.Context, ep endpoint, service, method string, serialization int, raw []byte, headers http.Header) ([]byte, error) {
				calls++
				require.Equal(t, body, raw)
				require.Equal(t, codec.SerializationTypeJSON, serialization)
				claims, err := authtest.Verify(c.config.Credentials, gatewayauth.Request{TargetNode: ep.target, Callee: service, Func: method, Body: raw}, headers, time.Now())
				require.NoError(t, err)
				if calls == 1 {
					require.Equal(t, "a", ep.target)
					nonce = claims.Nonce
					return nil, errs.NewFrameError(errs.RetServerNoService, "service moved")
				}
				require.Equal(t, "b", ep.target)
				require.NotEqual(t, nonce, claims.Nonce)
				return raw, nil
			}
			response, err := c.Forward(context.Background(), secretService, method, codec.SerializationTypeJSON, body)
			if method == "GetSecret" {
				require.NoError(t, err)
				require.Equal(t, body, response)
				require.Equal(t, 2, calls)
			} else {
				require.Error(t, err)
				require.Equal(t, 1, calls)
			}
			require.GreaterOrEqual(t, source.calls, 2, "route failure refreshes even for a write")
		})
	}
}

func TestInternalSelectsLocalOtherwiseTLSWithPrivateCA(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "b", "a")}
	config := internalConfig(t, source, "b")
	ca := config.CAFile
	c := newTestClient(t, config)
	ep, err := c.endpoint(context.Background(), secretService, "")
	require.NoError(t, err)
	require.Equal(t, endpoint{address: "127.0.0.1:11108", target: "b", direct: true}, ep, "本机优先：直连服务目录里的服务端口，不经本机网关")
	ep, err = c.endpoint(context.Background(), secretService, "b")
	require.NoError(t, err)
	require.Equal(t, endpoint{address: "a.example.test:11003", target: "a", caFile: ca, serverName: "a", compress: true}, ep, "跨主机调用默认压缩")
	for _, invalid := range []string{"", "none", "root"} {
		config.CAFile = invalid
		_, err = New(config)
		require.Error(t, err)
	}
}

// 目标服务部署在本机时直连它的回环端口：不签名、不压缩，也不查方法名单；跨主机仍要签名并查名单。
func TestLocalServiceIsCalledDirectlyWithoutSignature(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "b", "a")}
	c := newTestClient(t, internalConfig(t, source, "b"))
	// monitor 不在 UpdateSecret 的调用方名单里。
	require.False(t, c.catalog.Allowed("monitor", secretService, "UpdateSecret"))
	body := []byte(`{"key":"value"}`)
	metadata := CallMetadata{SpaceID: "space-1", UserID: "user-1", TraceID: "trace-1"}
	var endpoints []endpoint
	var seen []http.Header
	c.invoke = func(_ context.Context, ep endpoint, service, method string, _ int, raw []byte, headers http.Header) ([]byte, error) {
		require.Equal(t, secretService, service)
		require.Equal(t, body, raw)
		endpoints, seen = append(endpoints, ep), append(seen, headers)
		return raw, nil
	}
	for _, method := range []string{"GetSecret", "UpdateSecret"} {
		response, err := c.Forward(WithCallMetadata(context.Background(), metadata), secretService, method, codec.SerializationTypeJSON, body)
		require.NoError(t, err, method)
		require.Equal(t, body, response)
	}
	require.Len(t, endpoints, 2)
	for i, ep := range endpoints {
		require.Equal(t, endpoint{address: "127.0.0.1:11108", target: "b", direct: true}, ep)
		require.False(t, ep.compress, "本机直连不压缩")
		require.Empty(t, ep.caFile, "本机直连不用 TLS")
		require.Empty(t, signatureHeaders(seen[i]), "本机直连不带任何签名头")
		require.Equal(t, http.Header{"X-Space-Id": {"space-1"}, "X-User-Id": {"user-1"}, "X-Trace-Id": {"trace-1"}}, seen[i])
	}
	// 没有调用元数据时一个头也不带。
	_, err := c.Forward(context.Background(), secretService, "UpdateSecret", codec.SerializationTypeJSON, body)
	require.NoError(t, err)
	require.Empty(t, seen[2])

	// 测试缝把直连地址换成临时监听地址。
	restore := SetLocalServiceAddressForTest(func(port int) string {
		require.Equal(t, 11108, port, "传入的是服务目录里的服务端口")
		return "127.0.0.1:1"
	})
	_, err = c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypeJSON, body)
	restore()
	require.NoError(t, err)
	require.Equal(t, endpoint{address: "127.0.0.1:1", target: "b", direct: true}, endpoints[3])
	ep, err := c.endpoint(context.Background(), secretService, "")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:11108", ep.address, "恢复后回到目录端口")

	// 指定远端主机：名单里没有的方法在客户端就被拦下，名单里有的方法要签名并压缩。
	remote := WithCallMetadata(context.Background(), CallMetadata{HostID: "a"})
	calls := len(endpoints)
	_, err = c.Forward(remote, secretService, "UpdateSecret", codec.SerializationTypeJSON, body)
	require.ErrorContains(t, err, "not allowed")
	require.Len(t, endpoints, calls, "被拦下的调用不发出")
	require.True(t, c.catalog.Allowed("monitor", "trpc.moox.ops.SysDeploy", "GetCatalog"))
	c.invoke = func(_ context.Context, ep endpoint, service, method string, _ int, raw []byte, headers http.Header) ([]byte, error) {
		require.Equal(t, endpoint{address: "a.example.test:11003", target: "a", caFile: c.config.CAFile, serverName: "a", compress: true}, ep)
		_, err := authtest.Verify(c.config.Credentials, gatewayauth.Request{TargetNode: "a", Callee: service, Func: method, Body: raw}, headers, time.Now())
		require.NoError(t, err)
		return raw, nil
	}
	_, err = c.Forward(remote, "trpc.moox.ops.SysDeploy", "GetCatalog", codec.SerializationTypeJSON, body)
	require.NoError(t, err)
}

// 直连本机服务的调用没有签名，本机服务返回与网关拒签同码的错误时不重签重发；写方法只发一次。
func TestLocalDirectCallNeverResendsOnRejectionCode(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a")}
	c := newTestClient(t, internalConfig(t, source, "a"))
	calls, fetches := 0, source.calls
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		calls++
		return nil, gatewayRejection()
	}
	_, err := c.Forward(context.Background(), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Equal(t, gatewayauth.RetRejected, int(errs.Code(err)))
	require.Equal(t, 1, calls)
	require.Equal(t, fetches, source.calls, "不为重发刷新目录")
}

// gatewayRejection 是客户端解码后的网关拒签：被调方的框架错误。
func gatewayRejection() error {
	rejection := errs.NewFrameError(gatewayauth.RetRejected, "host gateway rejected the request signature or caller").(*errs.Error)
	rejection.Type = errs.ErrorTypeCalleeFramework
	return rejection
}

func TestEndpointHonorsRequestedHost(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "b", "a")}
	c := newTestClient(t, internalConfig(t, source, "b"))
	ctx := WithCallMetadata(context.Background(), CallMetadata{HostID: "a"})
	ep, err := c.endpoint(ctx, secretService, "")
	require.NoError(t, err)
	require.Equal(t, "a", ep.target, "指定主机时不再本机优先")
	require.False(t, ep.direct)
	ep, err = c.endpoint(ctx, secretService, "a")
	require.NoError(t, err)
	require.Equal(t, "a", ep.target, "只有一台候选时重试仍落在指定主机")
	_, err = c.endpoint(WithCallMetadata(context.Background(), CallMetadata{HostID: "zz"}), secretService, "")
	require.ErrorContains(t, err, "not deployed on host zz")
}

func TestExternalWhitelistAndTunnelLoopbackBoundary(t *testing.T) {
	config := Config{Mode: External, Credentials: authtest.Identity("moox-skill"), AccessAddress: "access.example.test:11004", AccessInstanceID: "access@storage"}
	c := newTestClient(t, config)
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		t.Fatal("forbidden call reached transport")
		return nil, nil
	}
	_, err := c.Forward(context.Background(), secretService, "GetSecretValue", codec.SerializationTypePB, nil)
	require.ErrorContains(t, err, "not allowed")
	config.AccessInstanceID = "storage"
	_, err = New(config)
	require.Error(t, err)
	config.AccessInstanceID = "access@storage"
	config.Source = &sourceStub{}
	_, err = New(config)
	require.ErrorContains(t, err, "does not use directory")
	tunnel := tunnelConfig(&sourceStub{directory: testDirectory(t, "a")})
	tunnel.Tunnels = tunnelFunc(func(context.Context, string) (string, error) { return "192.0.2.1:11002", nil })
	c = newTestClient(t, tunnel)
	_, err = c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypeJSON, nil)
	require.ErrorContains(t, err, "non-loopback")
	_, err = NewLocalDirectorySource("192.0.2.1:11002", time.Second)
	require.Error(t, err)
}

func TestExternalReadsResignRetriesAndWritesRunOnce(t *testing.T) {
	for _, call := range []struct {
		caller, service, method string
		attempts                int
	}{
		{"moox-skill", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", 2},
		// 周期提交声明为幂等写：请求可能已发出的网络失败后重发一次；其他写方法绝不重发。
		{"scf-collector", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", 2},
		{"scf-collector", "trpc.moox.storage.PrimaryStore", "UpsertFields", 1},
	} {
		t.Run(call.method, func(t *testing.T) {
			config := Config{Mode: External, Credentials: authtest.Identity(call.caller),
				AccessAddress: "access.example.test:11004", AccessInstanceID: "access@storage"}
			client := newTestClient(t, config)
			body := []byte{0x0a, 1, 'a', 0x0a, 1, 'b'}
			attempts := 0
			nonces := map[string]bool{}
			client.invoke = func(_ context.Context, ep endpoint, service, method string, serialization int, raw []byte, headers http.Header) ([]byte, error) {
				attempts++
				require.Equal(t, endpoint{address: config.AccessAddress, target: config.AccessInstanceID, compress: true}, ep)
				require.Equal(t, codec.SerializationTypePB, serialization)
				require.Equal(t, body, raw)
				claims, err := authtest.Verify(config.Credentials, gatewayauth.Request{TargetNode: config.AccessInstanceID, Callee: service, Func: method, Body: raw}, headers, time.Now())
				require.NoError(t, err)
				require.False(t, nonces[claims.Nonce], "every send must use a fresh nonce")
				nonces[claims.Nonce] = true
				if attempts == 1 {
					return nil, errs.NewFrameError(errs.RetClientNetErr, "unknown remote outcome")
				}
				return raw, nil
			}
			response, err := client.Forward(context.Background(), call.service, call.method, codec.SerializationTypePB, body)
			if call.attempts == 1 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, body, response)
			}
			require.Equal(t, call.attempts, attempts)
		})
	}
}

type sourceFunc func(context.Context, string) (DirectoryUpdate, error)

func (f sourceFunc) Fetch(ctx context.Context, version string) (DirectoryUpdate, error) {
	return f(ctx, version)
}

func TestRefreshWaitingOnAnotherFetchHonorsContext(t *testing.T) {
	c := newTestClient(t, tunnelConfig(&sourceStub{directory: testDirectory(t, "a")}))
	started := make(chan struct{})
	finish := make(chan struct{})
	done := make(chan error, 1)
	c.config.Source = sourceFunc(func(ctx context.Context, _ string) (DirectoryUpdate, error) {
		close(started)
		select {
		case <-finish:
			return DirectoryUpdate{}, errors.New("done")
		case <-ctx.Done():
			return DirectoryUpdate{}, ctx.Err()
		}
	})
	go func() { done <- c.Refresh(context.Background()) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, c.Refresh(ctx), context.DeadlineExceeded)
	close(finish)
	require.Error(t, <-done)
}

func TestWithdrawalAppliesWhenCacheCannotBeReplaced(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a", "b")}
	config := tunnelConfig(source)
	config.CachePath = filepath.Join(t.TempDir(), "directory.json")
	c := newTestClient(t, config)
	// Simulate a persist failure after a valid directory has been accepted.
	require.NoError(t, os.Remove(config.CachePath))
	require.NoError(t, os.Mkdir(config.CachePath, 0o700))
	source.directory = testDirectory(t, "b")
	require.ErrorContains(t, c.Refresh(context.Background()), "persist directory")
	ep, err := c.endpoint(context.Background(), secretService, "")
	require.NoError(t, err)
	require.Equal(t, "b", ep.target, "a withdrawn host must not remain active after a disk failure")
	require.NoError(t, c.Close())
	_, err = c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypePB, nil)
	require.ErrorIs(t, err, net.ErrClosed)
	require.ErrorIs(t, c.Refresh(context.Background()), net.ErrClosed)
}

func TestBusinessErrorsDoNotRetryOrRefresh(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a", "b")}
	c := newTestClient(t, tunnelConfig(source))
	calls, fetches := 0, source.calls
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		calls++
		return nil, errs.New(12345, "business error")
	}
	_, err := c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypePB, nil)
	require.EqualValues(t, 12345, errs.Code(err))
	require.Equal(t, 1, calls)
	require.Equal(t, fetches, source.calls)
}

func TestInvokeSanitizesMalformedResponseAndDoesNotRetryDecode(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a")}
	config := tunnelConfig(source)
	config.Serialization = codec.SerializationTypeJSON
	c := newTestClient(t, config)
	calls, fetches := 0, source.calls
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		calls++
		return []byte("recognizable-secret-response"), nil
	}
	err := c.Invoke(t.Context(), secretService, "GetSecret", &emptypb.Empty{}, &emptypb.Empty{})
	require.EqualValues(t, errs.RetClientDecodeFail, errs.Code(err))
	require.NotContains(t, err.Error(), "recognizable-secret-response")
	require.Equal(t, 1, calls)
	require.Equal(t, fetches, source.calls)
}

// 目录已应用到内存但落盘失败后，即使版本不再变化，也要在磁盘恢复后补写缓存。
func TestRefreshRetriesPersistingUnchangedDirectoryAfterCacheWriteFailure(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a")}
	config := tunnelConfig(source)
	dir := filepath.Join(t.TempDir(), "gatewayclient")
	config.CachePath = filepath.Join(dir, "directory.json")
	c := newTestClient(t, config)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		t.Skip("目录权限不能阻止写入（可能以 root 运行）")
	}
	source.directory = testDirectory(t, "a", "b")
	require.ErrorContains(t, c.Refresh(context.Background()), "persist directory")
	require.Equal(t, source.directory.Version, c.Directory().Version, "新目录必须已生效")
	require.NoError(t, os.Chmod(dir, 0o700))
	require.NoError(t, c.Refresh(context.Background()))
	cached, err := readCache(config.CachePath)
	require.NoError(t, err)
	require.Equal(t, source.directory.Version, cached.Version, "磁盘恢复后缓存应补写为当前目录")
}

// 网关自己拒签（专用错误码）说明请求没有到达服务：刷新目录后重新签名重发一次，写方法也一样；重发用新的 nonce。
func TestGatewayRejectionIsResignedAndResentOnceForAllMethods(t *testing.T) {
	for _, method := range []string{"GetSecret", "UpdateSecret"} {
		source := &sourceStub{directory: testDirectory(t, "a")}
		c := newTestClient(t, tunnelConfig(source))
		refreshed := source.calls
		var nonces []string
		c.invoke = func(_ context.Context, _ endpoint, _, _ string, _ int, raw []byte, headers http.Header) ([]byte, error) {
			nonces = append(nonces, headers.Get("X-Moox-Nonce"))
			if len(nonces) == 1 {
				return nil, gatewayRejection()
			}
			return raw, nil
		}
		_, err := c.Forward(context.Background(), secretService, method, codec.SerializationTypeJSON, []byte(`{}`))
		require.NoError(t, err, method)
		require.Len(t, nonces, 2, method)
		require.NotEqual(t, nonces[0], nonces[1], "重发必须重新签名")
		require.Greater(t, source.calls, refreshed, "重发前刷新目录")
	}
}

// 重签之后仍被拒就报错，不再重发；调用方声明不重试时一次也不重发。
func TestGatewayRejectionIsNotResentTwice(t *testing.T) {
	c := newTestClient(t, tunnelConfig(&sourceStub{directory: testDirectory(t, "a")}))
	calls := 0
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		calls++
		return nil, gatewayRejection()
	}
	_, err := c.Forward(context.Background(), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Equal(t, gatewayauth.RetRejected, int(errs.Code(err)))
	require.Equal(t, 2, calls)
	calls = 0
	_, err = c.Forward(WithoutRetry(context.Background()), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

// 别的错误不重发：服务自己返回的鉴权类错误、业务错误，对写方法都只发一次（对只读方法也不因鉴权错误重发）。
func TestServiceErrorsAreNotResentAsGatewayRejections(t *testing.T) {
	for _, failure := range []error{
		errs.NewFrameError(errs.RetServerAuthFail, "service authentication failed"),
		errs.New(12345, "business error"),
		// 下游服务透传回来的同码错误是业务错误：请求已经到过服务，不能当成网关拒签重发。
		errs.New(gatewayauth.RetRejected, "downstream returned the gateway rejection code"),
		// 本进程自己产生的同码框架错误也不是网关发的。
		errs.NewFrameError(gatewayauth.RetRejected, "local framework error"),
	} {
		for _, method := range []string{"GetSecret", "UpdateSecret"} {
			c := newTestClient(t, tunnelConfig(&sourceStub{directory: testDirectory(t, "a")}))
			calls := 0
			c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
				calls++
				return nil, failure
			}
			_, err := c.Forward(context.Background(), secretService, method, codec.SerializationTypeJSON, []byte(`{}`))
			require.Error(t, err)
			require.Equal(t, 1, calls, "%s %v", method, failure)
		}
	}
}

func TestWithoutRetryLeavesRetriesToTheCaller(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "b", "a")}
	c := newTestClient(t, tunnelConfig(source))
	calls := 0
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		calls++
		return nil, errs.NewFrameError(errs.RetClientNetErr, "boom")
	}
	body := []byte(`{}`)
	_, err := c.Forward(WithoutRetry(context.Background()), secretService, "GetSecret", codec.SerializationTypeJSON, body)
	require.Error(t, err)
	require.Equal(t, 1, calls, "只读调用在 WithoutRetry 下也只发一次")
	calls = 0
	_, err = c.Forward(context.Background(), secretService, "GetSecret", codec.SerializationTypeJSON, body)
	require.Error(t, err)
	require.Equal(t, 2, calls, "默认仍会重试一次")
}

type relayTunnelStub struct{ tunnelFunc }

func (relayTunnelStub) ResolveRelay(_ context.Context, control string) (string, error) {
	return "127.0.0.1:23456", nil
}

// 只能出站的主机经 control 中转：其他主机连 control 的公网端口，control 上的调用方连本机端口，
// 隧道模式经 SSH 转发到 control；缺 CA 或隧道不支持中转时拒绝。目标主机自己直连本机服务。
// control 不随目录下发：它就是部署了 admin 的那台。
func TestRelayEndpointForOutboundHost(t *testing.T) {
	directory := versioned(t, DirectoryUpdate{
		Hosts: []servicecatalog.Host{
			{ID: "control", Address: "control.example.test", Status: servicecatalog.Enabled},
			{ID: "storage", Address: "storage.example.test", Status: servicecatalog.Enabled},
			{ID: "home", Address: "192.168.0.103", Status: servicecatalog.Enabled, Reachability: servicecatalog.ReachabilityOutbound},
		},
		Deployments: []servicecatalog.Deployment{
			{HostID: "control", ComponentID: "admin", Status: servicecatalog.Enabled},
			{HostID: "home", ComponentID: "monitor", Status: servicecatalog.Enabled},
		},
	})
	require.Equal(t, servicecatalog.DirectoryHost{Address: "192.168.0.103", Relay: "control"}, resolved(t, directory).Hosts["home"])
	config := internalConfig(t, &sourceStub{directory: directory}, "storage")
	ca := config.CAFile
	route := func(config Config) (endpoint, error) {
		return newTestClient(t, config).endpoint(context.Background(), monitorService, "")
	}
	ep, err := route(config)
	require.NoError(t, err)
	require.Equal(t, "home.moox-relay:11003", ep.address)
	require.Equal(t, "home", ep.serverName)
	require.True(t, ep.compress)
	require.Equal(t, &relayRoute{controlAddress: "control.example.test:11003", controlHost: "control", target: "home", caFile: ca, credentials: config.Credentials}, ep.relay)

	config.LocalHostID, config.Credentials = "control", authtest.Component("monitor", "control")
	ep, err = route(config)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:11003", ep.relay.controlAddress)

	config.LocalHostID, config.Credentials = "home", authtest.Component("monitor", "home")
	ep, err = route(config)
	require.NoError(t, err)
	require.Equal(t, endpoint{address: "127.0.0.1:11410", target: "home", direct: true}, ep, "目标主机自己直连本机服务")

	tunnel := tunnelConfig(&sourceStub{directory: directory})
	_, err = route(tunnel)
	require.ErrorContains(t, err, "CA")
	tunnel.CAFile = ca
	_, err = route(tunnel)
	require.ErrorContains(t, err, "relay")
	tunnel.Tunnels = relayTunnelStub{tunnelFunc(func(context.Context, string) (string, error) { return "", errors.New("no direct tunnel") })}
	ep, err = route(tunnel)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:23456", ep.relay.controlAddress)
}
