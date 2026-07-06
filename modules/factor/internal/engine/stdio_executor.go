package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// StdioConfig configures one Python stdio worker process.
type StdioConfig struct {
	PythonBin     string
	WorkerPath    string
	FactorsDir    string
	SectionsDir   string
	Encoding      string
	TaskTimeout   time.Duration
	MaxFrameBytes int64
}

// StdioExecutor executes tasks through one persistent Python worker.
type StdioExecutor struct {
	cfg    StdioConfig
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
}

// NewStdioExecutor starts a Python worker and waits for its ready frame.
func NewStdioExecutor(cfg StdioConfig) (*StdioExecutor, error) {
	applyStdioDefaults(&cfg)
	cmd := exec.Command(cfg.PythonBin, cfg.WorkerPath,
		"--factors-dir", cfg.FactorsDir,
		"--sections-dir", cfg.SectionsDir,
		"--encoding", cfg.Encoding,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go io.Copy(io.Discard, stderr)
	exec := &StdioExecutor{cfg: cfg, cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	ready, err := ReadFrame(exec.stdout, cfg.MaxFrameBytes)
	if err != nil {
		_ = exec.Close()
		return nil, fmt.Errorf("read worker ready frame: %w", err)
	}
	if ready.Type != FrameTypeReady {
		_ = exec.Close()
		return nil, fmt.Errorf("unexpected worker frame before ready: %d", ready.Type)
	}
	return exec, nil
}

// Execute sends one request to the worker. One worker processes one request at a time.
func (e *StdioExecutor) Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cfg.TaskTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.cfg.TaskTimeout)
		defer cancel()
	}
	meta, err := EncodeJSONRequestMeta(task, frame)
	if err != nil {
		return nil, nonRetryable("encode task: %w", err)
	}
	if err := WriteFrame(e.stdin, FrameTypeRequest, meta, nil); err != nil {
		return nil, retryable("write worker request: %w", err)
	}
	type readResult struct {
		frame *Frame
		err   error
	}
	ch := make(chan readResult, 1)
	go func() {
		f, err := ReadFrame(e.stdout, e.cfg.MaxFrameBytes)
		ch <- readResult{frame: f, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = e.kill()
		return nil, retryable("worker task timed out: %w", ctx.Err())
	case result := <-ch:
		if result.err != nil {
			return nil, retryable("read worker response: %w", result.err)
		}
		switch result.frame.Type {
		case FrameTypeResponse:
			return DecodeJSONResponse(result.frame.Meta)
		case FrameTypeError:
			return nil, nonRetryable("factor worker error: %s", result.frame.Meta["message"])
		default:
			return nil, retryable("unexpected worker frame type: %d", result.frame.Type)
		}
	}
}

// Close stops the underlying worker process.
func (e *StdioExecutor) Close() error {
	if e == nil || e.cmd == nil || e.cmd.Process == nil {
		return nil
	}
	_ = e.stdin.Close()
	_ = e.cmd.Process.Kill()
	_ = e.cmd.Wait()
	return nil
}

func (e *StdioExecutor) kill() error {
	if e == nil || e.cmd == nil || e.cmd.Process == nil {
		return nil
	}
	return e.cmd.Process.Kill()
}

func applyStdioDefaults(cfg *StdioConfig) {
	if cfg.PythonBin == "" {
		cfg.PythonBin = "python3"
	}
	if cfg.Encoding == "" {
		cfg.Encoding = "json"
	}
	if cfg.TaskTimeout == 0 {
		cfg.TaskTimeout = 30 * time.Second
	}
	if cfg.MaxFrameBytes == 0 {
		cfg.MaxFrameBytes = 64 << 20
	}
}
