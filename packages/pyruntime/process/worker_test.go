package process

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func TestNewStdioWorkerTaskTimeoutReapsHungHelloProcess(t *testing.T) {
	script := writeWorkerScript(t, "import time\ntime.sleep(60)\n")
	type startedWorker struct {
		worker *StdioWorker
		cmd    *exec.Cmd
	}
	started := make(chan startedWorker, 1)
	done := make(chan error, 1)
	go func() {
		_, err := newStdioWorker(context.Background(), Config{
			PythonBin: "python3", WorkerPath: script, TaskTimeout: 40 * time.Millisecond,
		}, func(worker *StdioWorker) {
			started <- startedWorker{worker: worker, cmd: worker.cmd}
		})
		done <- err
	}()
	var observed startedWorker
	select {
	case observed = <-started:
	case err := <-done:
		t.Fatalf("worker failed before HELLO timeout observation: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("worker process did not start")
	}

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(500 * time.Millisecond):
		_ = observed.worker.Close()
		<-done
		t.Fatal("HELLO did not honor TaskTimeout with a background caller")
	}
	assertReapedWorker(t, observed.worker, observed.cmd)
}

func TestStdioWorkerLoadTaskTimeoutReapsHungProcess(t *testing.T) {
	worker := newNonReadingStdioWorker(t)
	worker.cfg.TaskTimeout = 40 * time.Millisecond
	cmd := worker.cmd
	done := make(chan error, 1)
	go func() {
		done <- worker.Load(context.Background(), LoadRequest{LogicalID: "hung-load"})
	}()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(500 * time.Millisecond):
		require.NoError(t, cmd.Process.Kill())
		<-done
		t.Fatal("LOAD did not honor TaskTimeout with a background caller")
	}
	assertReapedWorker(t, worker, cmd)
}

func TestStdioWorkerTaskTimeoutInterruptsBlockedFrameWrite(t *testing.T) {
	tests := []struct {
		name string
		call func(*StdioWorker) error
	}{
		{
			name: "run",
			call: func(worker *StdioWorker) error {
				_, err := worker.Run(context.Background(), RunRequest{
					RequestID: "blocked-write",
					Payload:   make([]byte, 8<<20),
				})
				return err
			},
		},
		{
			name: "load",
			call: func(worker *StdioWorker) error {
				return worker.Load(context.Background(), LoadRequest{
					LogicalID: "blocked-write",
					Path:      strings.Repeat("x", 1<<20),
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker := newNonReadingStdioWorker(t)
			worker.cfg.TaskTimeout = 40 * time.Millisecond
			cmd := worker.cmd
			done := make(chan error, 1)
			go func() {
				done <- test.call(worker)
			}()

			select {
			case err := <-done:
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case <-time.After(750 * time.Millisecond):
				_ = cmd.Process.Kill()
				err := <-done
				t.Fatalf("blocked frame write ignored TaskTimeout: %v", err)
			}
			assertReapedWorker(t, worker, cmd)
		})
	}
}

func TestStdioWorkerFrameWriteFailureReapsProcess(t *testing.T) {
	worker, cmd := newSilentStdioWorker(t)
	require.NoError(t, worker.in.Close())

	_, err := worker.Run(context.Background(), RunRequest{RequestID: "write-failure"})
	require.Error(t, err)
	assertReapedWorker(t, worker, cmd)
}

func TestStdioWorkerResponseReadFailureReapsProcess(t *testing.T) {
	worker, cmd := newSilentStdioWorker(t)
	require.NoError(t, worker.out.Close())

	_, err := worker.Run(context.Background(), RunRequest{RequestID: "read-failure"})
	require.Error(t, err)
	assertReapedWorker(t, worker, cmd)
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

func newNonReadingStdioWorker(t *testing.T) *StdioWorker {
	t.Helper()
	script := writeWorkerScript(t, `
import json
import struct
import sys
import time

meta = json.dumps({
    "protocol_version": "moox.py/v1",
    "worker_version": "test",
    "python_version": sys.version.split()[0],
    "runtime_env_hash": "",
    "encodings": ["json"],
}).encode()
sys.stdout.buffer.write(b"MX" + bytes([1]) + struct.pack(">I", len(meta)) + meta + struct.pack(">Q", 0))
sys.stdout.buffer.flush()
time.sleep(60)
`)
	worker, err := NewStdioWorker(context.Background(), Config{
		PythonBin: "python3", WorkerPath: script, TaskTimeout: 2 * time.Second,
	})
	require.NoError(t, err)
	return worker
}

func writeWorkerScript(t *testing.T, source string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker.py")
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	return path
}

func assertReapedWorker(t *testing.T, worker *StdioWorker, cmd *exec.Cmd) {
	t.Helper()
	assert.Equal(t, StateDead, worker.State())
	assert.Nil(t, worker.cmd)
	assert.NotNil(t, cmd.ProcessState)
	require.NoError(t, worker.Close())
}
