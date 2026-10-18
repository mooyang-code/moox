package snapshot

import (
	"fmt"
	"strings"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestViewOwnsCompleteSnapshotAndEnforcesRotationExpiry(t *testing.T) {
	raw := testsnapshot.New(t, "storage", "storage-primary")
	credential := testsnapshot.Credential("collector")
	key := &pb.GatewayVerificationKey{Caller: "collector", KeyId: "rotated-collector", Secret: []byte(strings.Repeat("r", 64)), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()}
	raw.VerificationKeys = append(raw.VerificationKeys, key)
	testsnapshot.Rehash(t, raw)
	view, err := Build("storage", raw)
	require.NoError(t, err)
	request := gatewayauth.Request{Method: "POST", Path: "/trpc.moox.storage.PrimaryStore/ReadTimeSeriesRows", Callee: "trpc.moox.storage.PrimaryStore", Func: "ReadTimeSeriesRows", TargetNode: "storage", Body: []byte("bytes")}
	for _, cred := range []gatewayauth.Credentials{credential, {Caller: "collector", KeyID: key.KeyId, Secret: string(key.Secret)}} {
		headers, err := gatewayauth.Sign(cred, request, time.Now())
		require.NoError(t, err)
		_, err = view.Verify(request, headers, time.Now())
		require.NoError(t, err)
		if cred.KeyID == key.KeyId {
			_, err = view.Verify(request, headers, time.Unix(key.ExpiresAtUnix, 0))
			require.Error(t, err)
		}
	}
	route, ok := view.Resolve("trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows")
	require.True(t, ok)
	route.Callers[0] = "changed"
	route, ok = view.Resolve("trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows")
	require.True(t, ok)
	require.NotContains(t, route.Callers, "changed")
	raw.Hash = "changed"
	raw.VerificationKeys[0].Secret[0] ^= 1
	require.NotEqual(t, raw.Hash, view.Hash())
	copy := view.Proto()
	copy.VerificationKeys[0].Secret[0] ^= 1
	require.False(t, proto.Equal(copy, view.Proto()))
	for _, key := range view.Proto().VerificationKeys {
		require.NotContains(t, fmt.Sprintf("%+v %#v", view, view), string(key.Secret))
	}
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
		"missing keys": func(s *pb.HostGatewaySnapshot) { s.VerificationKeys = nil },
		"duplicate key": func(s *pb.HostGatewaySnapshot) {
			s.VerificationKeys = append(s.VerificationKeys, s.VerificationKeys[0])
		},
		"out of scope key":  func(s *pb.HostGatewaySnapshot) { s.VerificationKeys[0].Caller = "moox-skill" },
		"short secret":      func(s *pb.HostGatewaySnapshot) { s.VerificationKeys[0].Secret = []byte("short") },
		"negative expiry":   func(s *pb.HostGatewaySnapshot) { s.VerificationKeys[0].ExpiresAtUnix = -1 },
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
