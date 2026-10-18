package adminpb

import (
	"testing"

	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"google.golang.org/protobuf/proto"
)

func TestSnapshotHashCoversKeysAndDirectoryWithoutMutatingInput(t *testing.T) {
	message := &HostGatewaySnapshot{SchemaVersion: 1, HostId: "control", Hash: "previous", VerificationKeys: []*GatewayVerificationKey{{Caller: "console", KeyId: "current", Secret: []byte("fixture-key")}}, Directory: &directorypb.ServiceDirectory{Hosts: map[string]*directorypb.DirectoryHost{"storage": {Address: "192.0.2.2"}, "control": {Address: "192.0.2.1"}}}}
	before := proto.Clone(message)
	first, err := SnapshotHash(message)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(before, message) {
		t.Fatal("hash mutated input")
	}
	message.Hash = "another"
	message.Directory.Hosts = map[string]*directorypb.DirectoryHost{"control": {Address: "192.0.2.1"}, "storage": {Address: "192.0.2.2"}}
	second, err := SnapshotHash(message)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("hash field and map insertion order must not change identity")
	}
	for _, mutate := range []func(*HostGatewaySnapshot){
		func(m *HostGatewaySnapshot) { m.VerificationKeys[0].Secret[0] ^= 1 },
		func(m *HostGatewaySnapshot) { m.VerificationKeys[0].KeyId = "rotated" },
		func(m *HostGatewaySnapshot) { m.VerificationKeys[0].ExpiresAtUnix = 123 },
		func(m *HostGatewaySnapshot) { m.Directory.Hosts["storage"].Address = "192.0.2.3" },
		func(m *HostGatewaySnapshot) { m.Disabled = true },
	} {
		changed := proto.Clone(message).(*HostGatewaySnapshot)
		mutate(changed)
		hash, err := SnapshotHash(changed)
		if err != nil {
			t.Fatal(err)
		}
		if hash == first {
			t.Fatal("snapshot content change did not change hash")
		}
	}
	if _, err := SnapshotHash(nil); err == nil {
		t.Fatal("nil snapshot was accepted")
	}
}
