package jobqueue

import (
	"context"
	"net"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
)

func TestEmbeddedJetStreamCreatesCloudNodeStreams(t *testing.T) {
	port := freeTCPPort(t)
	cfg := config.JetStreamConfig{
		Enabled:          true,
		NATSURL:          "nats://127.0.0.1:" + port,
		SubjectPrefix:    DefaultSubjectPrefix,
		ExecStream:       DefaultExecStream,
		ProjectionStream: DefaultProjectionStream,
		Embedded: config.EmbeddedJetStreamConfig{
			Enabled:          true,
			Host:             "127.0.0.1",
			Port:             mustAtoi(t, port),
			StoreDir:         t.TempDir(),
			StartupTimeoutMS: 5000,
		},
	}

	rt, err := StartEmbedded(context.Background(), cfg.Embedded)
	if err != nil {
		t.Fatalf("StartEmbedded() error = %v", err)
	}
	defer rt.Close()

	if err := rt.EnsureStreams(cfg); err != nil {
		t.Fatalf("EnsureStreams() error = %v", err)
	}

	execInfo, err := rt.JetStream().StreamInfo(DefaultExecStream)
	if err != nil {
		t.Fatalf("exec stream missing: %v", err)
	}
	if got, want := execInfo.Config.Subjects, []string{"moox.cloudnode.exec.v1.>"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("exec subjects = %v, want %v", got, want)
	}

	projectionInfo, err := rt.JetStream().StreamInfo(DefaultProjectionStream)
	if err != nil {
		t.Fatalf("projection stream missing: %v", err)
	}
	if got, want := projectionInfo.Config.Subjects, []string{"moox.cloudnode.projection.v1.>"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("projection subjects = %v, want %v", got, want)
	}
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	return port
}

func mustAtoi(t *testing.T, raw string) int {
	t.Helper()
	var out int
	for _, r := range raw {
		if r < '0' || r > '9' {
			t.Fatalf("invalid port %q", raw)
		}
		out = out*10 + int(r-'0')
	}
	return out
}
