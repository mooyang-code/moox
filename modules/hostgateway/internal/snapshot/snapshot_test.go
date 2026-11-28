package snapshot

import (
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	storageService = "trpc.moox.storage.PrimaryStore"
	storageMethod  = "ReadTimeSeriesRows"
)

func TestViewOwnsCompleteSnapshotAndVerifiesByHostKey(t *testing.T) {
	raw := testsnapshot.New(t, "storage", "storage-primary")
	view, err := Build("storage", raw)
	require.NoError(t, err)
	require.False(t, view.Disabled())
	require.Empty(t, view.Unknown())
	require.Equal(t, len(view.Routes()), view.Count())
	request := gatewayauth.Request{Callee: storageService, Func: storageMethod, TargetNode: "storage", Body: []byte("bytes")}
	verify := func(credentials gatewayauth.Credentials) error {
		headers, err := gatewayauth.Sign(credentials, request, time.Now())
		require.NoError(t, err)
		_, err = view.Verify(request, headers, time.Now())
		return err
	}
	require.NoError(t, verify(testsnapshot.Credential("collector", "control")))
	require.NoError(t, verify(testsnapshot.Credential("storage-primary", "storage")))
	require.NoError(t, verify(testsnapshot.Credential("moox-cli", "")))
	// 调用方不在签名主机上、用别的主机的密钥自称这台主机、没登记的身份，都验不过；失败原因不外露。
	for _, credentials := range []gatewayauth.Credentials{
		testsnapshot.Credential("collector", "storage"),
		testsnapshot.Credential("storage-primary", "control"),
		{Caller: "collector", Host: "control", Key: testsnapshot.HostKey("storage")},
		testsnapshot.Credential("scf-collector", ""),
	} {
		err := verify(credentials)
		require.EqualError(t, err, "host gateway authentication failed")
	}
	// 路由在本机算出来：地址是本机回环加服务目录里的端口。
	route, ok := view.Resolve(storageService, storageMethod)
	require.True(t, ok)
	require.Equal(t, "storage-primary", route.ComponentID)
	require.True(t, strings.HasPrefix(route.Address, "127.0.0.1:"))
	require.Contains(t, route.Callers, "collector")
	route.Callers[0] = "changed"
	route, ok = view.Resolve(storageService, storageMethod)
	require.True(t, ok)
	require.NotContains(t, route.Callers, "changed")
	routes := view.Routes()
	routes[0].ServicePath = "changed"
	require.NotEqual(t, "changed", view.Routes()[0].ServicePath)
	// 视图持有自己的副本：改输入或改取出的副本都不影响它。
	raw.Hash = "changed"
	raw.Hosts[0].SignPublicKey[0] ^= 1
	require.NotEqual(t, raw.Hash, view.Hash())
	copy := view.Proto()
	copy.Hosts[0].SignPublicKey[0] ^= 1
	require.False(t, proto.Equal(copy, view.Proto()))
	require.NoError(t, verify(testsnapshot.Credential("collector", "control")))
}

// 身份公钥只认 moox-cli 与目录里的外部身份：登记成组件名的身份公钥被忽略，持有它的人不能不带签名主机冒充组件。
func TestIdentityKeyNamedLikeComponentIsIgnored(t *testing.T) {
	raw := testsnapshot.New(t, "storage", "storage-primary")
	rogue := testsnapshot.Credential("trade", "")
	raw.Identities = append(raw.Identities, &pb.SnapshotIdentity{Identity: "trade", SignPublicKey: rogue.PublicKey()})
	testsnapshot.Rehash(t, raw)
	view, err := Build("storage", raw)
	require.NoError(t, err)
	request := gatewayauth.Request{Callee: storageService, Func: storageMethod, TargetNode: "storage"}
	headers, err := gatewayauth.Sign(rogue, request, time.Now())
	require.NoError(t, err)
	_, err = view.Verify(request, headers, time.Now())
	require.Error(t, err)
	// 同一份快照里 moox-cli 的身份公钥照常有效。
	headers, err = gatewayauth.Sign(testsnapshot.Credential("moox-cli", ""), request, time.Now())
	require.NoError(t, err)
	_, err = view.Verify(request, headers, time.Now())
	require.NoError(t, err)
}

// 没登记公钥的主机（还没签发过身份包）留在快照里，但它的请求验不过；别的主机不受影响。
func TestHostWithoutPublicKeyCannotSign(t *testing.T) {
	raw := testsnapshot.New(t, "storage", "storage-primary")
	testsnapshot.Host(t, raw, "control").SignPublicKey = nil
	testsnapshot.Rehash(t, raw)
	view, err := Build("storage", raw)
	require.NoError(t, err)
	request := gatewayauth.Request{Callee: storageService, Func: storageMethod, TargetNode: "storage"}
	for credentials, accepted := range map[*gatewayauth.Credentials]bool{
		new(testsnapshot.Credential("collector", "control")):       false,
		new(testsnapshot.Credential("storage-primary", "storage")): true,
	} {
		headers, err := gatewayauth.Sign(*credentials, request, time.Now())
		require.NoError(t, err)
		_, err = view.Verify(request, headers, time.Now())
		require.Equal(t, accepted, err == nil, credentials.Caller)
	}
}

// 两边版本不一致时快照里会有本进程不认识的组件：忽略它，其余路由、目录与验签照常生效。
func TestUnknownComponentIsIgnoredAndTheRestStillWorks(t *testing.T) {
	baseline, err := Build("storage", testsnapshot.New(t, "storage", "storage-primary"))
	require.NoError(t, err)
	raw := testsnapshot.New(t, "storage", "storage-primary")
	raw.Deployments = append(raw.Deployments,
		&pb.SnapshotDeployment{HostId: "storage", ComponentId: "from-the-future", Enabled: true},
		&pb.SnapshotDeployment{HostId: "control", ComponentId: "from-the-future", Enabled: true},
		&pb.SnapshotDeployment{HostId: "control", ComponentId: "another-new-one", Enabled: false},
	)
	testsnapshot.Rehash(t, raw)
	view, err := Build("storage", raw)
	require.NoError(t, err)
	require.Equal(t, []string{"another-new-one", "from-the-future"}, view.Unknown())
	require.False(t, view.Disabled())
	require.Equal(t, baseline.Routes(), view.Routes())
	route, ok := view.Resolve(storageService, storageMethod)
	require.True(t, ok)
	require.Equal(t, "storage-primary", route.ComponentID)
	require.Equal(t, baseline.Directory().Services, view.Directory().Services)
	require.Equal(t, []string{"storage"}, view.Directory().Services[storageService])
	request := gatewayauth.Request{Callee: storageService, Func: storageMethod, TargetNode: "storage"}
	headers, err := gatewayauth.Sign(testsnapshot.Credential("collector", "control"), request, time.Now())
	require.NoError(t, err)
	_, err = view.Verify(request, headers, time.Now())
	require.NoError(t, err)
	// 不认识的组件名不能拿主机密钥签名。
	headers, err = gatewayauth.Sign(testsnapshot.Credential("from-the-future", "control"), request, time.Now())
	require.NoError(t, err)
	_, err = view.Verify(request, headers, time.Now())
	require.Error(t, err)
}

// 本机被停用：快照照收（网关要知道自己已停用），但没有任何路由。
func TestDisabledLocalHostHasNoRoutes(t *testing.T) {
	raw := testsnapshot.New(t, "storage", "storage-primary")
	testsnapshot.Host(t, raw, "storage").Enabled = false
	testsnapshot.Rehash(t, raw)
	view, err := Build("storage", raw)
	require.NoError(t, err)
	require.True(t, view.Disabled())
	require.Zero(t, view.Count())
	require.Empty(t, view.Routes())
	_, ok := view.Resolve(storageService, storageMethod)
	require.False(t, ok)
	// 停用的主机不在目录里，它上面的服务也不在。
	require.NotContains(t, view.Directory().Hosts, "storage")
	require.NotContains(t, view.Directory().Services, storageService)

	// 别的主机被停用不影响本机，只是目录里没有它。
	raw = testsnapshot.New(t, "storage", "storage-primary")
	testsnapshot.Host(t, raw, "control").Enabled = false
	testsnapshot.Rehash(t, raw)
	view, err = Build("storage", raw)
	require.NoError(t, err)
	require.False(t, view.Disabled())
	_, ok = view.Resolve(storageService, storageMethod)
	require.True(t, ok)
	require.NotContains(t, view.Directory().Hosts, "control")

	// 本机上被停用的部署没有路由。
	raw = testsnapshot.New(t, "storage", "storage-primary")
	testsnapshot.Deployment(t, raw, "storage", "storage-primary").Enabled = false
	testsnapshot.Rehash(t, raw)
	view, err = Build("storage", raw)
	require.NoError(t, err)
	require.False(t, view.Disabled())
	_, ok = view.Resolve(storageService, storageMethod)
	require.False(t, ok)
}

func TestRejectMalformedSnapshot(t *testing.T) {
	for name, mutate := range map[string]func(*pb.HostGatewaySnapshot){
		"old schema":         func(s *pb.HostGatewaySnapshot) { s.SchemaVersion = 1 },
		"future schema":      func(s *pb.HostGatewaySnapshot) { s.SchemaVersion = SchemaVersion + 1 },
		"no hosts":           func(s *pb.HostGatewaySnapshot) { s.Hosts = nil },
		"unknown fields":     func(s *pb.HostGatewaySnapshot) { s.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01}) },
		"local host missing": func(s *pb.HostGatewaySnapshot) { testsnapshot.Host(t, s, "storage").HostId = "elsewhere" },
		"nil host":           func(s *pb.HostGatewaySnapshot) { s.Hosts = append(s.Hosts, nil) },
		"invalid host id":    func(s *pb.HostGatewaySnapshot) { testsnapshot.Host(t, s, "control").HostId = "Bad Host" },
		"empty address":      func(s *pb.HostGatewaySnapshot) { testsnapshot.Host(t, s, "control").Address = "" },
		"invalid address":    func(s *pb.HostGatewaySnapshot) { testsnapshot.Host(t, s, "control").Address = "bad address/" },
		"invalid private address": func(s *pb.HostGatewaySnapshot) {
			testsnapshot.Host(t, s, "control").PrivateAddress = "bad address/"
		},
		"host unknown fields": func(s *pb.HostGatewaySnapshot) {
			testsnapshot.Host(t, s, "control").ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		},
		"duplicate host": func(s *pb.HostGatewaySnapshot) {
			s.Hosts = append(s.Hosts, proto.Clone(testsnapshot.Host(t, s, "control")).(*pb.SnapshotHost))
		},
		"unknown reachability": func(s *pb.HostGatewaySnapshot) { testsnapshot.Host(t, s, "control").Reachability = "relay" },
		"short host key":       func(s *pb.HostGatewaySnapshot) { testsnapshot.Host(t, s, "control").SignPublicKey = []byte("short") },
		"long host key": func(s *pb.HostGatewaySnapshot) {
			host := testsnapshot.Host(t, s, "control")
			host.SignPublicKey = append(host.SignPublicKey, 0)
		},
		"nil deployment": func(s *pb.HostGatewaySnapshot) { s.Deployments = append(s.Deployments, nil) },
		"deployment on unknown host": func(s *pb.HostGatewaySnapshot) {
			s.Deployments = append(s.Deployments, &pb.SnapshotDeployment{HostId: "elsewhere", ComponentId: "trade", Enabled: true})
		},
		"duplicate deployment": func(s *pb.HostGatewaySnapshot) {
			s.Deployments = append(s.Deployments, proto.Clone(s.Deployments[0]).(*pb.SnapshotDeployment))
		},
		"duplicate ignored identity": func(s *pb.HostGatewaySnapshot) {
			key := s.Identities[0].SignPublicKey
			s.Identities = append(s.Identities, &pb.SnapshotIdentity{Identity: "trade", SignPublicKey: key}, &pb.SnapshotIdentity{Identity: "trade", SignPublicKey: key})
		},
		"deployment unknown fields": func(s *pb.HostGatewaySnapshot) {
			s.Deployments[0].ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		},
		"nil identity":       func(s *pb.HostGatewaySnapshot) { s.Identities = append(s.Identities, nil) },
		"short identity key": func(s *pb.HostGatewaySnapshot) { s.Identities[0].SignPublicKey = []byte("short") },
		"empty identity key": func(s *pb.HostGatewaySnapshot) { s.Identities[0].SignPublicKey = nil },
		"duplicate identity": func(s *pb.HostGatewaySnapshot) {
			s.Identities = append(s.Identities, proto.Clone(s.Identities[0]).(*pb.SnapshotIdentity))
		},
		"identity unknown fields": func(s *pb.HostGatewaySnapshot) {
			s.Identities[0].ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		},
		"too many identities": func(s *pb.HostGatewaySnapshot) {
			for len(s.Identities) <= 64 {
				s.Identities = append(s.Identities, proto.Clone(s.Identities[0]).(*pb.SnapshotIdentity))
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := testsnapshot.New(t, "storage", "storage-primary")
			mutate(raw)
			testsnapshot.Rehash(t, raw)
			_, err := Build("storage", raw)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
	// 哈希不对：全零、内容改了没重算、空。
	for name, mutate := range map[string]func(*pb.HostGatewaySnapshot){
		"zero hash":     func(s *pb.HostGatewaySnapshot) { s.Hash = strings.Repeat("0", 64) },
		"empty hash":    func(s *pb.HostGatewaySnapshot) { s.Hash = "" },
		"stale address": func(s *pb.HostGatewaySnapshot) { testsnapshot.Host(t, s, "control").Address = "192.0.2.9" },
		"stale key":     func(s *pb.HostGatewaySnapshot) { testsnapshot.Host(t, s, "control").SignPublicKey[0] ^= 1 },
		"stale deployment": func(s *pb.HostGatewaySnapshot) {
			testsnapshot.Deployment(t, s, "storage", "storage-primary").Enabled = false
		},
		"stale identity": func(s *pb.HostGatewaySnapshot) { s.Identities[0].SignPublicKey[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			raw := testsnapshot.New(t, "storage", "storage-primary")
			mutate(raw)
			_, err := Build("storage", raw)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
	valid := testsnapshot.New(t, "storage", "storage-primary")
	for _, hostID := range []string{"", "Bad Host", "elsewhere"} {
		_, err := Build(hostID, valid)
		require.ErrorIs(t, err, ErrInvalid, hostID)
	}
	_, err := Build("storage", nil)
	require.ErrorIs(t, err, ErrInvalid)
}
