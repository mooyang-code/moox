package e2e_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"github.com/mooyang-code/moox/packages/pyruntime/snapshot"
	"github.com/mooyang-code/moox/packages/pyruntime/transport"
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
	store := snapshot.NewStore(root)
	want := transport.Table{Columns: []string{"close", "symbol"}, Rows: [][]any{{1.25, "BTC"}, {2.5, "ETH"}}}
	h, err := store.AcquireArrow(context.Background(), snapshot.Key{Namespace: "strategy", DataRevision: "r1", SchemaHash: "s1"}, want)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	if h.Hash == "" || h.Path == "" || h.Encoding != transport.ArrowMMap {
		t.Fatal(h)
	}
	mapped, err := store.Open(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Reader().NumRecords() != 1 {
		_ = mapped.Close()
		t.Fatal("expected one Arrow record batch")
	}
	_ = mapped.Close()
	stream, err := transport.EncodeArrowStream(want)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := transport.DecodeArrowStream(stream)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Rows[0][0] != 1.25 || !reflect.DeepEqual(decoded.Rows[1], []any{2.5, "ETH"}) {
		t.Fatalf("Arrow stream contract mismatch: %#v", decoded.Rows)
	}
	testPythonArrowContract(t, stream, h.Path)
	if err := protocol.ValidateHello(protocol.HelloExpectation{ProtocolVersion: protocol.VersionV1}, protocol.Hello{ProtocolVersion: protocol.VersionV1}); err != nil {
		t.Fatal(err)
	}
}

func testPythonArrowContract(t *testing.T, stream []byte, path string) {
	python := os.Getenv("PYRUNTIME_PYTHON")
	if python == "" {
		python = "python3"
	}
	if err := exec.Command(python, "-c", "import pyarrow").Run(); err != nil {
		t.Logf("%s has no pyarrow; set PYRUNTIME_PYTHON to run Go/Python Arrow E2E", python)
		return
	}
	streamScript := `import sys, pyarrow.ipc as ipc
table = ipc.open_stream(sys.stdin.buffer).read_all()
assert table.num_rows == 2 and table.column_names == ["close", "symbol"]
print("stream-ok")`
	cmd := exec.Command(python, "-c", streamScript)
	cmd.Stdin = bytes.NewReader(stream)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "stream-ok" {
		t.Fatalf("Python could not read Go Arrow stream: output=%q err=%v", out, err)
	}
	fileScript := `import sys, pyarrow as pa, pyarrow.ipc as ipc
source = pa.memory_map(sys.argv[1], "r")
table = ipc.open_file(source).read_all()
assert table.num_rows == 2 and table.column_names == ["close", "symbol"]
print("file-ok")`
	out, err = exec.Command(python, "-c", fileScript, path).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "file-ok" {
		t.Fatalf("Python could not read Go Arrow mmap file: output=%q err=%v", out, err)
	}
}
