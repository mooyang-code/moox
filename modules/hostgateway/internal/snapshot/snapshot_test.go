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

func TestViewOwnsCompleteSnapshotAndVerifiesByHostKey(t *testing.T) {
	raw := testsnapshot.New(t, "storage", "storage-primary")
	view, err := Build("storage", raw)
	require.NoError(t, err)
	request := gatewayauth.Request{Callee: "trpc.moox.storage.PrimaryStore", Func: "ReadTimeSeriesRows", TargetNode: "storage", Body: []byte("bytes")}
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
		{Caller: "collector", Host: "control", Key: testsnapshot.HostKey("storage")},
		testsnapshot.Credential("moox-skill", ""),
	} {
		err := verify(credentials)
		require.EqualError(t, err, "host gateway authentication failed")
	}
	route, ok := view.Resolve("trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows")
	require.True(t, ok)
	route.Callers[0] = "changed"
	route, ok = view.Resolve("trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows")
	require.True(t, ok)
	require.NotContains(t, route.Callers, "changed")
	// 视图持有自己的副本：改输入或改取出的副本都不影响它。
	raw.Hash = "changed"
	raw.VerificationKeys[0].PublicKey[0] ^= 1
	require.NotEqual(t, raw.Hash, view.Hash())
	copy := view.Proto()
	copy.VerificationKeys[0].PublicKey[0] ^= 1
	require.False(t, proto.Equal(copy, view.Proto()))
	require.NoError(t, verify(testsnapshot.Credential("collector", "control")))
}

// 身份公钥只认 moox-cli 与目录里的外部身份：登记成组件名的身份公钥被忽略，持有它的人不能不带签名主机冒充组件。
func TestIdentityKeyNamedLikeComponentIsIgnored(t *testing.T) {
	raw := testsnapshot.New(t, "storage", "storage-primary")
	rogue := testsnapshot.Credential("trade", "")
	raw.VerificationKeys = append(raw.VerificationKeys, &pb.GatewayVerificationKey{Identity: "trade", PublicKey: rogue.PublicKey()})
	testsnapshot.Rehash(t, raw)
	view, err := Build("storage", raw)
	require.NoError(t, err)
	request := gatewayauth.Request{Callee: "trpc.moox.storage.PrimaryStore", Func: "ReadTimeSeriesRows", TargetNode: "storage"}
	headers, err := gatewayauth.Sign(rogue, request, time.Now())
	require.NoError(t, err)
	_, err = view.Verify(request, headers, time.Now())
	require.Error(t, err)
}

func TestRejectSnapshotCorruptionAndPrivilegeExpansion(t *testing.T) {
	for name, mutate := range map[string]func(*pb.HostGatewaySnapshot){
		"host":                func(s *pb.HostGatewaySnapshot) { s.HostId = "elsewhere" },
		"schema":              func(s *pb.HostGatewaySnapshot) { s.SchemaVersion = 2 },
		"remote upstream":     func(s *pb.HostGatewaySnapshot) { s.Routes[0].Address = "192.0.2.1:11426" },
		"wrong loopback port": func(s *pb.HostGatewaySnapshot) { s.Routes[0].Address = "127.0.0.1:9999" },
		"timeout":             func(s *pb.HostGatewaySnapshot) { s.Routes[0].TimeoutMs++ },
		"body limit":          func(s *pb.HostGatewaySnapshot) { s.Routes[0].MaxBodyBytes++ },
		"read-only":           func(s *pb.HostGatewaySnapshot) { s.Routes[0].ReadOnly = !s.Routes[0].ReadOnly },
		"duplicate route":     func(s *pb.HostGatewaySnapshot) { s.Routes = append(s.Routes, s.Routes[0]) },
		"unknown component":   func(s *pb.HostGatewaySnapshot) { s.Routes[0].ComponentId = "unknown" },
		"wildcard method":     func(s *pb.HostGatewaySnapshot) { s.Routes[0].Method = "*" },
		"external caller":     func(s *pb.HostGatewaySnapshot) { s.Routes[0].Callers = append(s.Routes[0].Callers, "moox-skill") },
		"duplicate caller": func(s *pb.HostGatewaySnapshot) {
			s.Routes[0].Callers = append(s.Routes[0].Callers, s.Routes[0].Callers[0])
		},
		"duplicate key": func(s *pb.HostGatewaySnapshot) {
			s.VerificationKeys = append(s.VerificationKeys, s.VerificationKeys[0])
		},
		"host key with identity": func(s *pb.HostGatewaySnapshot) { s.VerificationKeys[0].Identity = "moox-cli" },
		"invalid host":           func(s *pb.HostGatewaySnapshot) { s.VerificationKeys[0].HostId = "Bad Host" },
		"short public key":       func(s *pb.HostGatewaySnapshot) { s.VerificationKeys[0].PublicKey = []byte("short") },
		"identity as host caller": func(s *pb.HostGatewaySnapshot) {
			s.VerificationKeys[0].Callers = append(s.VerificationKeys[0].Callers, "moox-cli")
		},
		"identity with callers": func(s *pb.HostGatewaySnapshot) {
			last := s.VerificationKeys[len(s.VerificationKeys)-1]
			last.Callers = []string{"trade"}
		},
		"directory missing": func(s *pb.HostGatewaySnapshot) { s.Directory = nil },
		"directory hash":    func(s *pb.HostGatewaySnapshot) { s.Directory.Version = "bad" },
		"disabled mismatch": func(s *pb.HostGatewaySnapshot) { s.Disabled = true },
		"unknown fields":    func(s *pb.HostGatewaySnapshot) { s.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01}) },
	} {
		t.Run(name, func(t *testing.T) {
			raw := testsnapshot.New(t, "storage", "storage-primary")
			mutate(raw)
			testsnapshot.Rehash(t, raw)
			_, err := Build("storage", raw)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
	raw := testsnapshot.New(t, "storage")
	raw.Hash = strings.Repeat("0", 64)
	_, err := Build("storage", raw)
	require.ErrorIs(t, err, ErrInvalid)
}
