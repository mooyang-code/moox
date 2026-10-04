package e2e_test

import (
	"context"
	"os"
	"testing"

	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
)

func TestRuntimePublishesModuleAndValidatesProtocol(t *testing.T) {
	root := t.TempDir()
	v, err := moduleregistry.NewSourcePublisher(root).Publish(context.Background(), moduleregistry.ModuleSource{Type: "strategy", LogicalID: "demo", Source: []byte("def run(*a): return {}")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(v.Path); err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateHello(
		protocol.HelloExpectation{ProtocolVersion: protocol.VersionV1, RequiredEncoding: protocol.EncodingJSON},
		protocol.Hello{ProtocolVersion: protocol.VersionV1, Encodings: []protocol.Encoding{protocol.EncodingJSON}},
	); err != nil {
		t.Fatal(err)
	}
}
