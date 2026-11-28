package router

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
)

const collectMgr = "trpc.moox.collector.CollectMgr"

var externalIdentities = []string{"scf-collector"}

type remoteFunc func(context.Context, string, string, int, []byte) ([]byte, error)

func (f remoteFunc) Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
	return f(ctx, service, method, serialization, body)
}

// entryFixture 是存储机的网关：本机部署 storage-primary，登记成外部入口，外部身份都登记了公钥；
// 另有一台也是外部入口的海外机，和不是入口的控制机。
func entryFixture(t *testing.T, forwarder Forwarder, remote RemoteForwarder) (*Proxy, *snapshot.State, *prometheus.Registry) {
	t.Helper()
	return entryFixtureWith(t, forwarder, remote, "storage-primary")
}

// entryFixtureWith 同 entryFixture，但本机只部署 components；没列出的组件都在控制机上。
func entryFixtureWith(t *testing.T, forwarder Forwarder, remote RemoteForwarder, components ...string) (*Proxy, *snapshot.State, *prometheus.Registry) {
	t.Helper()
	raw := testsnapshot.New(t, "storage", components...)
	testsnapshot.Host(t, raw, "storage").ExternalEntry = true
	testsnapshot.AddHost(t, raw, "overseas", "192.0.2.3", true)
	for _, identity := range externalIdentities {
		testsnapshot.AddIdentity(raw, identity)
	}
	state := &snapshot.State{}
	applySnapshot(t, state, raw)
	registry := prometheus.NewRegistry()
	p, err := NewService(ServiceOptions{State: state, Replay: gatewayauth.NewReplay(time.Now().Add(-time.Minute)), Forwarder: forwarder, Remote: remote, Registerer: registry})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Close()) })
	return p, state, registry
}

// call 直接调处理函数：external 为真时走外部入口（11004），否则走 11002、11003 的入口。
func call(p *Proxy, external bool, service, method string, body []byte, headers http.Header) ([]byte, error) {
	ctx, message := codec.WithNewMessage(context.Background())
	defer codec.PutBackMessage(message)
	message.WithServerRPCName("/" + service + "/" + method)
	message.WithSerializationType(codec.SerializationTypePB)
	metadata := codec.MetaData{}
	for key := range headers {
		metadata[key] = []byte(headers.Get(key))
	}
	message.WithServerMetaData(metadata)
	handle := p.handle
	if external {
		handle = p.handleExternal
	}
	response, err := handle(nil, ctx, func(input interface{}) (filter.ServerChain, error) {
		input.(*codec.Body).Data = body
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return response.(*codec.Body).Data, nil
}

func sign(t *testing.T, credentials gatewayauth.Credentials, service, method string, body []byte) http.Header {
	t.Helper()
	headers, err := testrpc.Signed(credentials, "storage", service, method, body)
	require.NoError(t, err)
	return headers
}

// denials 从指标登记表里读一组标签的拒绝次数。
func denials(t *testing.T, registry *prometheus.Registry, caller, service, method, reason string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	want := map[string]string{"caller": caller, "service": service, "method": method, "reason": reason}
	for _, family := range families {
		if family.GetName() != "moox_gateway_external_denials_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			matched := len(metric.GetLabel()) == len(want)
			for _, label := range metric.GetLabel() {
				matched = matched && want[label.GetName()] == label.GetValue()
			}
			if matched {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// 外部入口：外部身份调名单上的方法，目标在本机就转本机；往后只带空间与追踪两个头。
func TestExternalEntryForwardsLocalServiceWithOnlySpaceAndTrace(t *testing.T) {
	var seen codec.MetaData
	p, _, _ := entryFixture(t, forwardFunc(func(_ context.Context, route servicecatalog.Route, _ int, body []byte, metadata codec.MetaData) ([]byte, error) {
		require.Equal(t, "127.0.0.1:20102", route.Address)
		seen = metadata
		return body, nil
	}), remoteFunc(func(context.Context, string, string, int, []byte) ([]byte, error) {
		t.Fatal("本机有的服务不该转给别的主机")
		return nil, nil
	}))
	body := []byte{0x0a, 1, 'a'}
	headers := sign(t, testsnapshot.Credential("scf-collector", ""), primary, "CommitTimeSeriesBatch", body)
	headers.Set("X-Space-Id", "crypto")
	headers.Set("X-Trace-Id", "trace-1")
	headers.Set("X-User-Id", "someone")
	headers.Set(gatewayauth.HeaderOriginCaller, "moox-cli")
	headers.Set(gatewayauth.HeaderVerifiedHost, "control")
	response, err := call(p, true, primary, "CommitTimeSeriesBatch", body, headers)
	require.NoError(t, err)
	require.Equal(t, body, response)
	require.Equal(t, codec.MetaData{"X-Space-Id": []byte("crypto"), "X-Trace-Id": []byte("trace-1")}, seen)
}

// 外部入口：目标不在本机时交给进程内的网关客户端（它以 host-gateway 的身份带来源头转给目标主机）。
// 这里存储机是入口，storage-primary 却部署在控制机上。
func TestExternalEntryForwardsRemoteServiceThroughGatewayClient(t *testing.T) {
	var calls atomic.Int32
	p, _, _ := entryFixtureWith(t, forwardFunc(func(context.Context, servicecatalog.Route, int, []byte, codec.MetaData) ([]byte, error) {
		t.Fatal("storage-primary 不在入口主机上")
		return nil, nil
	}), remoteFunc(func(_ context.Context, service, method string, _ int, body []byte) ([]byte, error) {
		calls.Add(1)
		require.Equal(t, primary, service)
		require.Equal(t, "UpsertFields", method)
		return body, nil
	}))
	body := []byte{0x0a, 1, 'x'}
	response, err := call(p, true, primary, "UpsertFields", body, sign(t, testsnapshot.Credential("scf-collector", ""), primary, "UpsertFields", body))
	require.NoError(t, err)
	require.Equal(t, body, response)
	require.EqualValues(t, 1, calls.Load())
}

// 外部入口只接外部身份调名单上的方法：名单外的方法、组件、moox-cli、重放、改过的请求都在转发之前被拒并计数。
func TestExternalEntryRejectsEverythingButListedExternalCalls(t *testing.T) {
	var calls atomic.Int32
	p, state, registry := entryFixture(t, forwardFunc(func(_ context.Context, _ servicecatalog.Route, _ int, body []byte, _ codec.MetaData) ([]byte, error) {
		calls.Add(1)
		return body, nil
	}), remoteFunc(func(_ context.Context, _, _ string, _ int, body []byte) ([]byte, error) {
		calls.Add(1)
		return body, nil
	}))
	scf := testsnapshot.Credential("scf-collector", "")
	for name, tc := range map[string]struct {
		credentials     gatewayauth.Credentials
		service, method string
	}{
		"scf-collector 读行情（不在名单上）": {scf, primary, "ReadTimeSeriesRows"},
		"scf-collector 调采集名单干预":    {scf, collectMgr, "SetSubjectExclusions"},
		"已删除的 moox-skill 走外部入口":    {testsnapshot.Credential("moox-skill", ""), primary, "ReadTimeSeriesRows"},
		"控制机上的 collector 走外部入口":    {testsnapshot.Credential("collector", "control"), primary, "ReadTimeSeriesRows"},
		"moox-cli 走外部入口":           {testsnapshot.Credential("moox-cli", ""), primary, "ReadTimeSeriesRows"},
		"入口主机自己的 host-gateway":     {testsnapshot.Credential("host-gateway", "storage"), primary, "ReadTimeSeriesRows"},
		"没登记公钥的身份":                 {testsnapshot.Credential("unknown-tool", ""), primary, "ReadTimeSeriesRows"},
	} {
		_, err := call(p, true, tc.service, tc.method, nil, sign(t, tc.credentials, tc.service, tc.method, nil))
		requireRejected(t, err, name)
	}
	require.EqualValues(t, 2, denials(t, registry, "scf-collector", primary, "ReadTimeSeriesRows", "permission")+denials(t, registry, "scf-collector", collectMgr, "SetSubjectExclusions", "permission"))
	require.EqualValues(t, 2, denials(t, registry, "unknown", primary, "ReadTimeSeriesRows", "authentication"), "没登记公钥或已删除的身份验签不过")
	// 签名之后改了请求体。
	_, err := call(p, true, primary, "CommitTimeSeriesBatch", []byte("altered"), sign(t, scf, primary, "CommitTimeSeriesBatch", []byte("original")))
	requireRejected(t, err, "请求体被改")
	require.Zero(t, calls.Load())
	// 同一个签名第二次出现。
	headers := sign(t, scf, primary, "CommitTimeSeriesBatch", nil)
	_, err = call(p, true, primary, "CommitTimeSeriesBatch", nil, headers)
	require.NoError(t, err)
	_, err = call(p, true, primary, "CommitTimeSeriesBatch", nil, headers)
	requireRejected(t, err, "重放")
	require.EqualValues(t, 1, denials(t, registry, "scf-collector", primary, "CommitTimeSeriesBatch", "replay"))
	require.EqualValues(t, 1, calls.Load())
	// 登记里不再是外部入口：一律不接。
	raw := state.Load().Proto()
	testsnapshot.Host(t, raw, "storage").ExternalEntry = false
	applySnapshot(t, state, raw)
	_, err = call(p, true, primary, "CommitTimeSeriesBatch", nil, sign(t, scf, primary, "CommitTimeSeriesBatch", nil))
	require.ErrorContains(t, err, "external entry is not enabled")
	require.EqualValues(t, 1, calls.Load())
}

// 外部身份的签名拿到 11003 上直接用不行：它们只能从外部入口进来。
func TestExternalIdentityCannotUseInternalPorts(t *testing.T) {
	p, _, _ := entryFixture(t, forwardFunc(func(context.Context, servicecatalog.Route, int, []byte, codec.MetaData) ([]byte, error) {
		t.Fatal("不应到达上游")
		return nil, nil
	}), nil)
	_, err := call(p, false, primary, "CommitTimeSeriesBatch", nil, sign(t, testsnapshot.Credential("scf-collector", ""), primary, "CommitTimeSeriesBatch", nil))
	requireRejected(t, err, "scf-collector 直接到 11003")
}

// 来源头的三条检查（设计 3.2）：签名的是外部入口主机的 host-gateway、头里是外部身份、该身份在方法名单上，缺一条就拒绝并计数。
func TestOriginCallerHeaderRequiresEntryHostExternalIdentityAndListedMethod(t *testing.T) {
	var calls atomic.Int32
	var seen codec.MetaData
	p, state, registry := entryFixture(t, forwardFunc(func(_ context.Context, _ servicecatalog.Route, _ int, body []byte, metadata codec.MetaData) ([]byte, error) {
		calls.Add(1)
		seen = metadata
		return body, nil
	}), nil)
	entryGateway := testsnapshot.Credential("host-gateway", "overseas")
	forwarded := func(credentials gatewayauth.Credentials, origin, method string) error {
		headers := sign(t, credentials, primary, method, nil)
		headers.Set(gatewayauth.HeaderOriginCaller, origin)
		_, err := call(p, false, primary, method, nil, headers)
		return err
	}
	// 正向：海外机（外部入口）的网关替 scf-collector 转一次写入。
	require.NoError(t, forwarded(entryGateway, "scf-collector", "CommitTimeSeriesBatch"))
	require.EqualValues(t, 1, calls.Load())
	_, leaked := seen[gatewayauth.HeaderOriginCaller]
	require.False(t, leaked, "来源头不往被调服务传")

	// 第一条：签名主机不是外部入口，或签名的不是 host-gateway。
	requireRejected(t, forwarded(testsnapshot.Credential("host-gateway", "control"), "scf-collector", "CommitTimeSeriesBatch"), "控制机不是外部入口")
	requireRejected(t, forwarded(testsnapshot.Credential("collector", "control"), "scf-collector", "CommitTimeSeriesBatch"), "带来源头的不是 host-gateway（哪怕它自己在名单上）")
	require.EqualValues(t, 2, denials(t, registry, "scf-collector", primary, "CommitTimeSeriesBatch", "origin_host"))
	// 第二条：头里不是外部身份。
	for _, origin := range []string{"moox-cli", "moox-skill", "console", "collector", "host-gateway", "", "unknown"} {
		requireRejected(t, forwarded(entryGateway, origin, "CommitTimeSeriesBatch"), "来源头是 "+origin)
	}
	require.EqualValues(t, 7, denials(t, registry, "unknown", primary, "CommitTimeSeriesBatch", "origin_identity"))
	// 第三条：外部身份不在这个方法的名单上。
	requireRejected(t, forwarded(entryGateway, "scf-collector", "ReadTimeSeriesRows"), "scf-collector 只能写")
	require.EqualValues(t, 1, denials(t, registry, "scf-collector", primary, "ReadTimeSeriesRows", "origin_method"))
	// 没有来源头时 host-gateway 只能调它自己名单里的方法（GatewayControl 的两个）。
	_, err := call(p, false, primary, "CommitTimeSeriesBatch", nil, sign(t, entryGateway, primary, "CommitTimeSeriesBatch", nil))
	requireRejected(t, err, "不带来源头的 host-gateway")
	require.EqualValues(t, 1, calls.Load(), "被拒的请求都没到上游")

	// 入口主机被停用后，它转过来的请求不再认。
	raw := state.Load().Proto()
	testsnapshot.Host(t, raw, "overseas").Enabled = false
	applySnapshot(t, state, raw)
	requireRejected(t, forwarded(entryGateway, "scf-collector", "CommitTimeSeriesBatch"), "停用的入口主机")
}
