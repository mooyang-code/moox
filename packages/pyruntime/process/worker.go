package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateBusy     State = "busy"
	StateDead     State = "dead"
)

type LoadRequest struct {
	LogicalID, SourceHash, Path string
	ModuleType                  string
	EntryPoint                  string
}
type RunRequest struct {
	RequestID, ModuleType, LogicalID, SourceHash string
	Encoding                                     protocol.Encoding
	Meta                                         json.RawMessage
	Payload                                      []byte
}
type RunResult struct {
	Meta    json.RawMessage
	Payload []byte
}
type Worker interface {
	Load(context.Context, LoadRequest) error
	Run(context.Context, RunRequest) (RunResult, error)
	State() State
	Close() error
}

type LogRecord struct {
	RequestID, LogicalID, SourceHash, Stream, Message string
	Truncated                                         bool
}

type StdioWorker struct {
	cfg   Config
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   io.ReadCloser
	state State
	hello protocol.Hello
	mu    sync.Mutex
	logs  chan LogRecord
}

func NewStdioWorker(ctx context.Context, cfg Config) (*StdioWorker, error) {
	return newStdioWorker(ctx, cfg, nil)
}

func newStdioWorker(ctx context.Context, cfg Config, observeStarted func(*StdioWorker)) (*StdioWorker, error) {
	cfg.defaults()
	if cfg.WorkerPath == "" {
		return nil, errors.New("pyruntime: worker path is required")
	}
	args := append([]string{cfg.WorkerPath}, cfg.Args...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The worker process is resident for the lifetime of its supervisor. The
	// request context only bounds startup/handshake work; task cancellation is
	// handled by Run and must not tear down a healthy resident process.
	cmd := exec.Command(cfg.PythonBin, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errOut, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w := &StdioWorker{cfg: cfg, cmd: cmd, in: in, out: out, state: StateStarting, logs: make(chan LogRecord, 32)}
	if observeStarted != nil {
		observeStarted(w)
	}
	go w.captureStderr(errOut)
	type helloResult struct {
		frame protocol.Frame
		err   error
	}
	helloCh := make(chan helloResult, 1)
	go func() {
		frame, err := protocol.ReadFrame(out, cfg.Limits)
		helloCh <- helloResult{frame: frame, err: err}
	}()
	startupCtx, cancelStartup := context.WithTimeout(ctx, cfg.TaskTimeout)
	defer cancelStartup()
	var frame protocol.Frame
	select {
	case <-startupCtx.Done():
		_ = w.Close()
		return nil, startupCtx.Err()
	case result := <-helloCh:
		frame, err = result.frame, result.err
	}
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("hello: %w", err)
	}
	if frame.Type != protocol.TypeHello {
		_ = w.Close()
		return nil, fmt.Errorf("hello: unexpected frame %d", frame.Type)
	}
	hello, err := protocol.DecodeHello(frame.Meta)
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := protocol.ValidateHello(cfg.Hello, hello); err != nil {
		_ = w.Close()
		return nil, err
	}
	w.state = StateReady
	w.hello = hello
	return w, nil
}

func (w *StdioWorker) Load(ctx context.Context, req LoadRequest) error {
	if strings.TrimSpace(req.SourceHash) == "" {
		return errors.New("pyruntime: load source hash is required")
	}
	return w.control(ctx, protocol.TypeLoad, map[string]any{"logical_id": req.LogicalID, "source_hash": req.SourceHash, "path": req.Path, "module_type": req.ModuleType, "entrypoint": req.EntryPoint})
}
func (w *StdioWorker) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != StateReady {
		return RunResult{}, errors.New("pyruntime: worker not ready")
	}
	w.state = StateBusy
	defer func() {
		if w.state != StateDead {
			w.state = StateReady
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, w.cfg.TaskTimeout)
	defer cancel()
	fields, err := decodeRunMeta(req.Meta)
	if err != nil {
		return RunResult{}, err
	}
	fields["request_id"], fields["module_type"], fields["logical_id"], fields["source_hash"], fields["encoding"] = req.RequestID, req.ModuleType, req.LogicalID, req.SourceHash, req.Encoding
	meta, err := json.Marshal(fields)
	if err != nil {
		return RunResult{}, err
	}
	if err := w.writeFrameLocked(ctx, protocol.Frame{Type: protocol.TypeRun, Meta: meta, Payload: req.Payload}); err != nil {
		return RunResult{}, err
	}
	ch := make(chan struct {
		f   protocol.Frame
		err error
	}, 1)
	out := w.out
	go func() {
		f, e := protocol.ReadFrame(out, w.cfg.Limits)
		ch <- struct {
			f   protocol.Frame
			err error
		}{f, e}
	}()
	select {
	case <-ctx.Done():
		return RunResult{}, errors.Join(ctx.Err(), w.terminateLocked())
	case result := <-ch:
		if result.err != nil {
			return RunResult{}, errors.Join(result.err, w.terminateLocked())
		}
		if result.f.Type == protocol.TypeError {
			err := fmt.Errorf("python worker error: %s", result.f.Meta)
			return RunResult{}, errors.Join(err, w.terminateLocked())
		}
		if result.f.Type != protocol.TypeResult {
			err := fmt.Errorf("unexpected result frame: %d", result.f.Type)
			return RunResult{}, errors.Join(err, w.terminateLocked())
		}
		return RunResult{Meta: result.f.Meta, Payload: result.f.Payload}, nil
	}
}

func decodeRunMeta(raw json.RawMessage) (map[string]any, error) {
	fields := map[string]any{}
	if len(raw) == 0 {
		return fields, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil, fmt.Errorf("decode run meta: %w", err)
	}
	if fields == nil {
		return nil, errors.New("decode run meta: expected JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode run meta: trailing JSON value")
		}
		return nil, fmt.Errorf("decode run meta: %w", err)
	}
	return fields, nil
}

func (w *StdioWorker) control(ctx context.Context, typ protocol.MessageType, meta map[string]any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == StateDead {
		return errors.New("pyruntime: worker dead")
	}
	ctx, cancel := context.WithTimeout(ctx, w.cfg.TaskTimeout)
	defer cancel()
	b, _ := json.Marshal(meta)
	if err := w.writeFrameLocked(ctx, protocol.Frame{Type: typ, Meta: b}); err != nil {
		return err
	}
	done := make(chan error, 1)
	out := w.out
	go func() {
		f, e := protocol.ReadFrame(out, w.cfg.Limits)
		if e == nil && f.Type != protocol.TypeResult {
			if f.Type == protocol.TypeError {
				e = fmt.Errorf("python worker error: %s", f.Meta)
			} else {
				e = fmt.Errorf("unexpected control response: %d", f.Type)
			}
		}
		done <- e
	}()
	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), w.terminateLocked())
	case err := <-done:
		if err != nil {
			return errors.Join(err, w.terminateLocked())
		}
		return nil
	}
}

// writeFrameLocked requires w.mu. The write runs separately so TaskTimeout can
// still terminate a child that stopped reading stdin while the caller retains
// exclusive use of this worker.
func (w *StdioWorker) writeFrameLocked(ctx context.Context, frame protocol.Frame) error {
	done := make(chan error, 1)
	in := w.in
	go func() {
		done <- protocol.WriteFrame(in, w.cfg.Limits, frame)
	}()
	select {
	case err := <-done:
		if err != nil {
			return errors.Join(err, w.terminateLocked())
		}
		return nil
	case <-ctx.Done():
		terminateErr := w.terminateLocked()
		<-done
		return errors.Join(ctx.Err(), terminateErr)
	}
}

func (w *StdioWorker) State() State { w.mu.Lock(); defer w.mu.Unlock(); return w.state }
func (w *StdioWorker) Hello() protocol.Hello {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hello
}
func (w *StdioWorker) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.terminateLocked()
}

// terminateLocked requires w.mu and consumes the command so Wait is called once.
func (w *StdioWorker) terminateLocked() error {
	cmd := w.cmd
	in := w.in
	out := w.out
	w.cmd = nil
	w.in = nil
	w.out = nil
	w.state = StateDead
	if in != nil {
		_ = in.Close()
	}
	if out != nil {
		_ = out.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killErr := cmd.Process.Kill()
	killed := killErr == nil
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if killed && errors.As(waitErr, &exitErr) {
		waitErr = nil
	}
	return errors.Join(killErr, waitErr)
}
func (w *StdioWorker) captureStderr(r io.Reader) {
	buf := make([]byte, w.cfg.MaxLogBytes)
	n, _ := io.ReadFull(r, buf)
	if n > 0 {
		w.logs <- LogRecord{Stream: "stderr", Message: string(buf[:n]), Truncated: n == len(buf)}
	}
}
func (w *StdioWorker) Logs() <-chan LogRecord { return w.logs }

var _ = time.Second
var _ = os.ErrProcessDone
