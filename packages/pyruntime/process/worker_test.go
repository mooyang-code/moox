package process

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupervisorRequiresFactory(t *testing.T) {
	s := NewSupervisor(func(context.Context) (Worker, error) { return nil, context.Canceled }, SupervisorConfig{})
	if _, err := s.Ensure(context.Background()); err == nil {
		t.Fatal("expected factory error")
	}
}

func TestStdioWorkerCloseReapsProcessAndIsIdempotent(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60")
	require.NoError(t, cmd.Start())
	w := &StdioWorker{cmd: cmd, state: StateReady}

	require.NoError(t, w.Close())
	assert.Equal(t, StateDead, w.State())
	assert.Nil(t, w.cmd)
	assert.NotNil(t, cmd.ProcessState)

	require.NoError(t, w.Close())
}

func TestStdioWorkerRunTimeoutReapsProcess(t *testing.T) {
	w, cmd := newSilentStdioWorker(t)
	t.Cleanup(func() { _ = w.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := w.Run(ctx, RunRequest{RequestID: "timeout"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, StateDead, w.State())
	assert.Nil(t, w.cmd)
	assert.NotNil(t, cmd.ProcessState)
	require.NoError(t, w.Close())
}

func TestStdioWorkerLoadCancellationReapsProcess(t *testing.T) {
	w, cmd := newSilentStdioWorker(t)
	t.Cleanup(func() { _ = w.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.Load(ctx, LoadRequest{LogicalID: "cancel"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, StateDead, w.State())
	assert.Nil(t, w.cmd)
	assert.NotNil(t, cmd.ProcessState)
	require.NoError(t, w.Close())
}

func TestStdioWorkerRunPreservesLargeMetaNumbers(t *testing.T) {
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	t.Cleanup(func() {
		_ = requestReader.Close()
		_ = requestWriter.Close()
		_ = responseReader.Close()
		_ = responseWriter.Close()
	})
	cfg := Config{TaskTimeout: time.Second}
	cfg.defaults()
	worker := &StdioWorker{cfg: cfg, in: requestWriter, out: responseReader, state: StateReady}

	frameCh := make(chan protocol.Frame, 1)
	errCh := make(chan error, 1)
	go func() {
		frame, err := protocol.ReadFrame(requestReader, cfg.Limits)
		frameCh <- frame
		if err == nil {
			err = protocol.WriteFrame(responseWriter, cfg.Limits, protocol.Frame{
				Type: protocol.TypeResult,
				Meta: json.RawMessage(`{"ok":true}`),
			})
		}
		errCh <- err
	}()

	_, err := worker.Run(context.Background(), RunRequest{
		RequestID: "authoritative-request", ModuleType: "factor",
		LogicalID: "Factor", SourceHash: "hash", Encoding: protocol.EncodingJSON,
		Meta: json.RawMessage(
			`{"request_id":"stale","large":9007199254740993,"huge":1e400}`,
		),
	})
	require.NoError(t, err)
	frame := <-frameCh
	require.NoError(t, <-errCh)
	require.Contains(t, string(frame.Meta), `"large":9007199254740993`)
	require.Contains(t, string(frame.Meta), `"huge":1e400`)

	var fields map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(frame.Meta)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&fields))
	require.Equal(t, "authoritative-request", fields["request_id"])
	require.Equal(t, "factor", fields["module_type"])
	require.Equal(t, "Factor", fields["logical_id"])
	require.Equal(t, "hash", fields["source_hash"])
	require.Equal(t, "json", fields["encoding"])
}

func TestStdioWorkerRunRejectsNonObjectOrTrailingMeta(t *testing.T) {
	for _, raw := range []string{`[]`, `{} {}`} {
		worker := &StdioWorker{cfg: Config{TaskTimeout: time.Second}, state: StateReady}
		_, err := worker.Run(context.Background(), RunRequest{Meta: json.RawMessage(raw)})
		require.Error(t, err)
		require.Equal(t, StateReady, worker.State())
	}
}

func newSilentStdioWorker(t *testing.T) (*StdioWorker, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command("python3", "-c", "import time; time.sleep(60)")
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	cfg := Config{TaskTimeout: time.Second}
	cfg.defaults()
	return &StdioWorker{
		cfg:   cfg,
		cmd:   cmd,
		in:    in,
		out:   out,
		state: StateReady,
	}, cmd
}
