package e2e_test

import (
	"context"
	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"github.com/mooyang-code/moox/packages/pyruntime/snapshot"
	"os"
	"testing"
)

func TestRuntimePublishesAndSharesSnapshot(t *testing.T) {
	root := t.TempDir()
	v, err := moduleregistry.NewSourcePublisher(root).Publish(context.Background(), moduleregistry.ModuleSource{Type: "strategy", LogicalID: "demo", Source: []byte("def run(*a): return {}")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(v.Path); err != nil {
		t.Fatal(err)
	}
	h, err := snapshot.NewStore(root).Acquire(context.Background(), snapshot.Key{Namespace: "strategy", DataRevision: "r1", SchemaHash: "s1"}, []byte(`{"close":1}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if h.Hash == "" || h.Path == "" {
		t.Fatal(h)
	}
	_ = h.Release()
	if err := protocol.ValidateHello(protocol.HelloExpectation{ProtocolVersion: protocol.VersionV1}, protocol.Hello{ProtocolVersion: protocol.VersionV1}); err != nil {
		t.Fatal(err)
	}
}
