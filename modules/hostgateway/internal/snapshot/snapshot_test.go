package snapshot

import (
	"strings"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"google.golang.org/protobuf/proto"
)

func validSnapshot(t *testing.T) []byte {
	t.Helper()
	built, err := testsnapshot.Build("storage", false, []gatewayroute.Route{{
		ServiceID: "storage-view", Address: "127.0.0.1:20103", ServicePath: "trpc.moox.storage.DataView",
		AllowedMethods: []string{"QueryTimeSeriesRows"}, AllowedCallers: []string{"strategy"},
	}}, []gatewayroute.VerificationKey{{KeyID: "strategy-1", Caller: "strategy", Secret: "s"}, {KeyID: "strategy-2", Caller: "strategy", Secret: "t"}},
		testsnapshot.Directory("storage", "trpc.moox.storage.DataView"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := proto.Marshal(built)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestValidateAcceptsConsistentSnapshot(t *testing.T) {
	built, err := testsnapshot.Build("storage", false, []gatewayroute.Route{{
		ServiceID: "storage-view", Address: "127.0.0.1:20103", ServicePath: "trpc.moox.storage.DataView",
		AllowedMethods: []string{"QueryTimeSeriesRows"}, AllowedCallers: []string{"strategy"},
	}}, []gatewayroute.VerificationKey{{KeyID: "strategy-1", Caller: "strategy", Secret: "s"}}, testsnapshot.Directory("storage", "trpc.moox.storage.DataView"))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Validate("storage", built)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Routes != 1 || applied.Registry == nil || applied.Directory.GetVersion() == "" {
		t.Fatalf("%+v", applied)
	}
	var current Current
	if current.Load() != nil || current.Hash() != "" {
		t.Fatal("空的 Current 应当没有快照")
	}
	current.Store(applied, time.Now())
	if current.Hash() != built.GetHash() {
		t.Fatal("Current 没有保存快照")
	}
}

func TestValidateRejectsTamperedSnapshots(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, []byte) (string, []byte)
		want   string
	}{
		{"其他主机的快照", func(_ *testing.T, raw []byte) (string, []byte) { return "compute-1", raw }, "属于主机"},
		{"篡改路由", func(t *testing.T, raw []byte) (string, []byte) {
			return "storage", mutate(t, raw, func(s *hostSnapshot) { s.Routes[0].Callers = []string{"console"} })
		}, "哈希不一致"},
		{"篡改密钥", func(t *testing.T, raw []byte) (string, []byte) {
			return "storage", mutate(t, raw, func(s *hostSnapshot) { s.Keys[0].Secret = "other" })
		}, "哈希不一致"},
		{"篡改目录", func(t *testing.T, raw []byte) (string, []byte) {
			return "storage", mutate(t, raw, func(s *hostSnapshot) { s.Directory.Hosts[0].Address = "1.2.3.4" })
		}, "服务目录版本与内容不一致"},
		{"缺少目录", func(t *testing.T, raw []byte) (string, []byte) {
			return "storage", mutate(t, raw, func(s *hostSnapshot) { s.Directory = nil })
		}, "缺少服务目录"},
		{"非法路由", func(t *testing.T, raw []byte) (string, []byte) {
			return "storage", mutate(t, raw, func(s *hostSnapshot) { s.Routes[0].Address = "10.0.0.1:20103" })
		}, "路由"},
		{"重复的 KeyID", func(t *testing.T, raw []byte) (string, []byte) {
			return "storage", mutate(t, raw, func(s *hostSnapshot) { s.Keys[1].KeyId = s.Keys[0].KeyId })
		}, "校验密钥"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hostID, raw := tc.mutate(t, validSnapshot(t))
			_, err := Validate(hostID, unmarshal(t, raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望报错包含 %q，实际 %v", tc.want, err)
			}
		})
	}
	if _, err := Validate("storage", nil); err == nil {
		t.Fatal("空快照应当报错")
	}
}

type hostSnapshot = adminpb.HostSnapshot

func unmarshal(t *testing.T, raw []byte) *adminpb.HostSnapshot {
	t.Helper()
	out := &adminpb.HostSnapshot{}
	if err := proto.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mutate(t *testing.T, raw []byte, change func(*hostSnapshot)) []byte {
	t.Helper()
	snapshot := unmarshal(t, raw)
	change(snapshot)
	encoded, err := proto.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
