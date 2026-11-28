package router

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

const primary = "trpc.moox.storage.PrimaryStore"

type forwardFunc func(context.Context, servicecatalog.Route, int, []byte, codec.MetaData) ([]byte, error)

func (f forwardFunc) Forward(ctx context.Context, route servicecatalog.Route, serialization int, body []byte, metadata codec.MetaData) ([]byte, error) {
	return f(ctx, route, serialization, body, metadata)
}

func proxyFixture(t *testing.T, forwarder Forwarder) (*Proxy, *snapshot.State) {
	t.Helper()
	state := &snapshot.State{}
	applySnapshot(t, state, testsnapshot.New(t, "storage", "storage-primary"))
	p, err := NewService(ServiceOptions{State: state, Replay: gatewayauth.NewReplay(time.Now().Add(-time.Minute)), Forwarder: forwarder})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Close()) })
	return p, state
}

func applySnapshot(t *testing.T, state *snapshot.State, raw *pb.HostGatewaySnapshot) {
	t.Helper()
	testsnapshot.Rehash(t, raw)
	view, err := snapshot.Build("storage", raw)
	require.NoError(t, err)
	state.Apply(view)
}

func dispatch(ctx context.Context, p *Proxy, method string, body []byte, headers http.Header) ([]byte, error) {
	ctx, message := codec.WithNewMessage(ctx)
	defer codec.PutBackMessage(message)
	message.WithServerRPCName("/" + primary + "/" + method)
	message.WithSerializationType(codec.SerializationTypePB)
	metadata := codec.MetaData{}
	for key := range headers {
		metadata[key] = []byte(headers.Get(key))
	}
	message.WithServerMetaData(metadata)
	response, err := p.handle(nil, ctx, func(input interface{}) (filter.ServerChain, error) {
		input.(*codec.Body).Data = body
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return response.(*codec.Body).Data, nil
}

// signed 以部署在 control 上的组件身份签名（夹具把被测主机之外的调用方都放在 control 上）。
func signed(t *testing.T, caller, method string, body []byte) http.Header {
	t.Helper()
	return signedBy(t, testsnapshot.Credential(caller, "control"), method, body)
}

func signedBy(t *testing.T, credentials gatewayauth.Credentials, method string, body []byte) http.Header {
	t.Helper()
	headers, err := testrpc.Signed(credentials, "storage", primary, method, body)
	require.NoError(t, err)
	return headers
}

// requireRejected 断言网关自己拒签：专用错误码，且不透露具体原因。
func requireRejected(t *testing.T, err error, message string) {
	t.Helper()
	require.Error(t, err, message)
	require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), message)
	require.NotContains(t, err.Error(), "nonce", message)
}

func TestProxyRejectsUnauthorizedOrAlteredCallsBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	p, _ := proxyFixture(t, forwardFunc(func(_ context.Context, route servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		calls.Add(1)
		require.Equal(t, "127.0.0.1:20102", route.Address)
		return body, nil
	}))
	// 调用名单之外的调用方、没有登记公钥的身份。
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "trade", "ReadTimeSeriesRows", nil))
	requireRejected(t, err, "trade 不在 ReadTimeSeriesRows 的名单里")
	_, err = dispatch(context.Background(), p, "CommitTimeSeriesBatch", nil, signedBy(t, testsnapshot.Credential("scf-collector", ""), "CommitTimeSeriesBatch", nil))
	requireRejected(t, err, "外部身份不能直接到主机网关")
	_, err = dispatch(context.Background(), p, "NotRegistered", nil, signed(t, "collector", "NotRegistered", nil))
	require.ErrorContains(t, err, "not deployed")
	// 签名之后改了请求体或方法。
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", []byte("altered"), signed(t, "collector", "ReadTimeSeriesRows", []byte("original")))
	requireRejected(t, err, "请求体被改")
	_, err = dispatch(context.Background(), p, "UpsertFields", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	requireRejected(t, err, "方法被改")
	require.Zero(t, calls.Load())
	body := []byte{0x0a, 1, 'a', 0x0a, 1, 'b'}
	headers := signed(t, "collector", "ReadTimeSeriesRows", body)
	response, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", body, headers)
	require.NoError(t, err)
	require.Equal(t, body, response)
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", body, headers)
	requireRejected(t, err, "同一个签名第二次出现")
	require.EqualValues(t, 1, calls.Load())
}

// 外部身份即使在快照里登记了公钥，在主机网关上也调不了任何方法（只能经外部入口）。
func TestProxyRejectsExternalIdentityEvenWithRegisteredKey(t *testing.T) {
	p, state := proxyFixture(t, forwardFunc(func(context.Context, servicecatalog.Route, int, []byte, codec.MetaData) ([]byte, error) {
		t.Fatal("外部身份的请求不应到达上游")
		return nil, nil
	}))
	raw := state.Load().Proto()
	scf := testsnapshot.Credential("scf-collector", "")
	raw.Identities = append(raw.Identities, &pb.SnapshotIdentity{Identity: "scf-collector", SignPublicKey: scf.PublicKey()})
	applySnapshot(t, state, raw)
	_, err := dispatch(context.Background(), p, "CommitTimeSeriesBatch", nil, signedBy(t, scf, "CommitTimeSeriesBatch", nil))
	requireRejected(t, err, "scf-collector 直接到主机网关")
}

// 别的主机的密钥不能冒充：自称另一台主机、自称没部署在本机的组件、冒充 moox-cli，都在转发之前被拒。
func TestProxyRejectsImpersonationWithAnotherHostsKey(t *testing.T) {
	var calls atomic.Int32
	p, _ := proxyFixture(t, forwardFunc(func(_ context.Context, _ servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		calls.Add(1)
		return body, nil
	}))
	storageKey := testsnapshot.HostKey("storage")
	for name, credentials := range map[string]gatewayauth.Credentials{
		"用存储机的密钥自称控制机上的 collector": {Caller: "collector", Host: "control", Key: storageKey},
		"collector 没有部署在存储机上":      {Caller: "collector", Host: "storage", Key: storageKey},
		"用主机密钥冒充 moox-cli":         {Caller: "moox-cli", Key: storageKey},
		"用主机密钥带主机头自称 moox-cli":     {Caller: "moox-cli", Host: "storage", Key: storageKey},
		"没有登记的主机":                  {Caller: "collector", Host: "unknown", Key: storageKey},
		"用 moox-cli 的密钥自称控制机上的组件":  {Caller: "collector", Host: "control", Key: testsnapshot.Credential("moox-cli", "").Key},
	} {
		_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signedBy(t, credentials, "ReadTimeSeriesRows", nil))
		requireRejected(t, err, name)
	}
	require.Zero(t, calls.Load(), "冒充的请求不应到达上游")
	// 对照：同样的请求由 moox-cli 用自己的密钥签名就能通过。
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signedBy(t, testsnapshot.Credential("moox-cli", ""), "ReadTimeSeriesRows", nil))
	require.NoError(t, err, "moox-cli 用自己的密钥")
	require.EqualValues(t, 1, calls.Load())
}

// 换密钥没有新旧并存：快照里换成新公钥后，旧私钥的签名立即被拒；新私钥通过。
func TestProxyRejectsOldKeyAfterRotation(t *testing.T) {
	p, state := proxyFixture(t, forwardFunc(func(_ context.Context, _ servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		return body, nil
	}))
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.NoError(t, err)
	rotated := authtest.Component("collector", "rotated-control")
	rotated.Host = "control"
	raw := state.Load().Proto()
	testsnapshot.Host(t, raw, "control").SignPublicKey = rotated.PublicKey()
	applySnapshot(t, state, raw)
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	requireRejected(t, err, "旧私钥")
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signedBy(t, rotated, "ReadTimeSeriesRows", nil))
	require.NoError(t, err)
}

// 防重放记录只在内存里：签名时间早于网关进程启动时间的请求被拒（重启前用过的签名不能在重启后重放）。
func TestProxyRejectsSignaturesOlderThanProcessStart(t *testing.T) {
	p, _ := proxyFixture(t, forwardFunc(func(_ context.Context, _ servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		return body, nil
	}))
	headers := signed(t, "collector", "ReadTimeSeriesRows", nil)
	p.options.Replay = gatewayauth.NewReplay(time.Now().Add(5 * time.Second))
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, headers)
	requireRejected(t, err, "重启前签的请求")
}

// 外面带进来的 x-moox- 头不往后传（包括调用方伪造的“已验签主机”头）；只有 GatewayControl 收到网关写的签名主机 ID。
func TestProxyStripsGatewayHeadersAndTellsGatewayControlTheVerifiedHost(t *testing.T) {
	var seen codec.MetaData
	p, _ := proxyFixture(t, forwardFunc(func(_ context.Context, _ servicecatalog.Route, _ int, body []byte, metadata codec.MetaData) ([]byte, error) {
		seen = metadata
		return body, nil
	}))
	headers := signed(t, "collector", "ReadTimeSeriesRows", nil)
	headers.Set("X-Moox-Verified-Host", "storage")
	headers.Set("x-moox-extra", "private")
	headers.Set("trace-fixture", "preserved")
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, headers)
	require.NoError(t, err)
	require.Equal(t, codec.MetaData{http.CanonicalHeaderKey("trace-fixture"): []byte("preserved")}, seen)

	inbound := codec.MetaData{"X-Moox-Verified-Host": []byte("control"), "x-moox-signature": []byte("s"), "trace": []byte("kept")}
	forwarded := upstreamMetadata(servicecatalog.GatewayControlPath, gatewayauth.Claims{Caller: "host-gateway", Host: "storage"}, inbound)
	require.Equal(t, codec.MetaData{gatewayauth.HeaderVerifiedHost: []byte("storage"), "trace": []byte("kept")}, forwarded, "只认验过的签名主机")
	// 非主机身份没有签名主机：头在，值为空，GatewayControl 据此拒绝。
	forwarded = upstreamMetadata(servicecatalog.GatewayControlPath, gatewayauth.Claims{Caller: "moox-cli"}, inbound)
	require.Equal(t, "", string(forwarded[gatewayauth.HeaderVerifiedHost]))
	// 其他服务（含 SecretMgr）一律不额外注入网关内部头，外面带进来的 x-moox- 头全部剥除。
	inbound["X-Moox-Verified-Caller"] = []byte("console")
	forwarded = upstreamMetadata("trpc.moox.ops.SecretMgr", gatewayauth.Claims{Caller: "trade", Host: "overseas"}, inbound)
	require.Equal(t, codec.MetaData{"trace": []byte("kept")}, forwarded)
	forwarded = upstreamMetadata("trpc.moox.storage.PrimaryStore", gatewayauth.Claims{Caller: "trade", Host: "overseas"}, inbound)
	require.Equal(t, codec.MetaData{"trace": []byte("kept")}, forwarded)
}

// 快照换了就立即生效，不用重启：组件从它所在的主机撤走后不能再用那台主机的密钥签名；本机的部署停用后路由消失。
func TestProxyAppliesDeploymentChangesWithoutRestart(t *testing.T) {
	p, state := proxyFixture(t, forwardFunc(func(_ context.Context, _ servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		return body, nil
	}))
	original := state.Load().Proto()
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "factor-mgr", "ReadTimeSeriesRows", nil))
	require.NoError(t, err)
	// factor-mgr 从 control 上撤走：路由名单里还有它，但 control 的主机密钥不能再替它签名。
	revoked := state.Load().Proto()
	testsnapshot.Deployment(t, revoked, "control", "factor-mgr").Enabled = false
	applySnapshot(t, state, revoked)
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "factor-mgr", "ReadTimeSeriesRows", nil))
	requireRejected(t, err, "撤走的组件")
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.NoError(t, err, "同一台主机上别的组件不受影响")
	applySnapshot(t, state, original)
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "factor-mgr", "ReadTimeSeriesRows", nil))
	require.NoError(t, err)
	// 本机的 storage-primary 停用：路由没了。
	removed := state.Load().Proto()
	testsnapshot.Deployment(t, removed, "storage", "storage-primary").Enabled = false
	applySnapshot(t, state, removed)
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.ErrorContains(t, err, "not deployed")
	require.Equal(t, errs.RetServerNoService, errs.Code(err), "客户端依赖该错误码刷新目录并重试")
}

// 快照里有本进程不认识的组件（两边版本不一致）：忽略它，认识的组件照常验签、转发。
func TestProxyForwardsKnownRoutesWhenSnapshotHasUnknownComponent(t *testing.T) {
	var calls atomic.Int32
	p, state := proxyFixture(t, forwardFunc(func(_ context.Context, route servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		calls.Add(1)
		require.Equal(t, "storage-primary", route.ComponentID)
		require.Equal(t, "127.0.0.1:20102", route.Address)
		return body, nil
	}))
	raw := state.Load().Proto()
	raw.Deployments = append(raw.Deployments,
		&pb.SnapshotDeployment{HostId: "storage", ComponentId: "from-the-future", Enabled: true},
		&pb.SnapshotDeployment{HostId: "control", ComponentId: "from-the-future", Enabled: true},
	)
	applySnapshot(t, state, raw)
	require.Equal(t, []string{"from-the-future"}, state.Load().Unknown())
	body := []byte{0x0a, 1, 'a'}
	response, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", body, signed(t, "collector", "ReadTimeSeriesRows", body))
	require.NoError(t, err)
	require.Equal(t, body, response)
	require.EqualValues(t, 1, calls.Load())
	// 不认识的组件名不是合法调用方。
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "from-the-future", "ReadTimeSeriesRows", nil))
	requireRejected(t, err, "不认识的组件")
	require.EqualValues(t, 1, calls.Load())
}

// 本机被停用：没有路由，签名正确的请求也到不了上游。
func TestProxyHasNoRoutesWhenLocalHostDisabled(t *testing.T) {
	p, state := proxyFixture(t, forwardFunc(func(context.Context, servicecatalog.Route, int, []byte, codec.MetaData) ([]byte, error) {
		t.Fatal("停用主机上的请求不应到达上游")
		return nil, nil
	}))
	raw := state.Load().Proto()
	testsnapshot.Host(t, raw, "storage").Enabled = false
	applySnapshot(t, state, raw)
	require.True(t, state.Load().Disabled())
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.Error(t, err)
}

func TestProxyBodyLimitsAndDeadlines(t *testing.T) {
	var calls atomic.Int32
	tooLarge := make([]byte, (32<<20)+1)
	p, _ := proxyFixture(t, forwardFunc(func(ctx context.Context, route servicecatalog.Route, _ int, _ []byte, _ codec.MetaData) ([]byte, error) {
		calls.Add(1)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), time.Duration(route.TimeoutMS)*time.Millisecond)
		return tooLarge, nil
	}))
	_, err := dispatch(context.Background(), p, "ReadTimeSeriesRows", tooLarge, signed(t, "collector", "ReadTimeSeriesRows", tooLarge))
	require.ErrorContains(t, err, "request exceeds")
	require.Zero(t, calls.Load())
	_, err = dispatch(context.Background(), p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.ErrorContains(t, err, "response exceeds")
	require.EqualValues(t, 1, calls.Load())
	p.options.Forwarder = forwardFunc(func(ctx context.Context, _ servicecatalog.Route, _ int, _ []byte, _ codec.MetaData) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = dispatch(ctx, p, "ReadTimeSeriesRows", nil, signed(t, "collector", "ReadTimeSeriesRows", nil))
	require.Equal(t, errs.RetClientTimeout, errs.Code(err))
}

// 上游不重试（只读调用的重发由调用方的 gatewayclient 负责）；交给它的元数据原样传给被调服务（x-moox- 头已由路由层筛掉）。
func TestUpstreamForwardsOnceWithMetadata(t *testing.T) {
	opened, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var calls atomic.Int32
	var invalidMetadata atomic.Bool
	testrpc.Serve(t, opened, primary, func(ctx context.Context, _ []byte) ([]byte, error) {
		calls.Add(1)
		metadata := codec.Message(ctx).ServerMetaData()
		if string(metadata["trace-fixture"]) != "preserved" || string(metadata[gatewayauth.HeaderVerifiedHost]) != "control" {
			invalidMetadata.Store(true)
		}
		return nil, errs.NewFrameError(errs.RetClientNetErr, "synthetic connection failure")
	})
	u := NewUpstream()
	defer u.Close()
	metadata := codec.MetaData{gatewayauth.HeaderVerifiedHost: []byte("control"), "trace-fixture": []byte("preserved")}
	for _, readOnly := range []bool{true, false} {
		calls.Store(0)
		method := "UpsertFields"
		if readOnly {
			method = "ReadTimeSeriesRows"
		}
		_, err := u.Forward(context.Background(), servicecatalog.Route{Address: opened.Addr().String(), ServicePath: primary, Method: method, ReadOnly: readOnly, TimeoutMS: 1000}, codec.SerializationTypePB, nil, metadata)
		require.Error(t, err)
		require.EqualValues(t, 1, calls.Load())
	}
	require.False(t, invalidMetadata.Load())
}
