package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

const (
	defaultShellTimeout = 60 * time.Second
	maxShellTimeout     = 5 * time.Minute
	maxShellOutput      = 1 << 20
)

type shellResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
}

func runShell(ctx context.Context, command, dir string, timeout time.Duration) shellResult {
	if timeout <= 0 {
		timeout = defaultShellTimeout
	}
	if timeout > maxShellTimeout {
		timeout = maxShellTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-c", command)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, n: maxShellOutput}
	cmd.Stderr = &limitedWriter{w: &stderr, n: maxShellOutput}
	err := cmd.Run()

	result := shellResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		TimedOut: runCtx.Err() == context.DeadlineExceeded,
	}
	if err == nil {
		return result
	}
	if result.TimedOut {
		result.ExitCode = 124
		result.Stderr = appendText(result.Stderr, "command timed out")
		return result
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
		return result
	}
	result.ExitCode = 1
	result.Stderr = appendText(result.Stderr, err.Error())
	return result
}

func appendText(base, extra string) string {
	if base == "" {
		return extra
	}
	return base + "\n" + extra
}

type limitedWriter struct {
	w     *bytes.Buffer
	n     int
	trunc bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	remain := l.n - l.w.Len()
	if remain <= 0 {
		l.trunc = true
		return len(p), nil
	}
	if len(p) > remain {
		_, _ = l.w.Write(p[:remain])
		l.trunc = true
		if !bytes.HasSuffix(l.w.Bytes(), []byte("\n[truncated]")) {
			_, _ = l.w.WriteString("\n[truncated]")
		}
		return len(p), nil
	}
	_, err := l.w.Write(p)
	return len(p), err
}

func formatShell(result shellResult) string {
	return fmt.Sprintf("exit_code=%d timed_out=%t\n--- stdout ---\n%s\n--- stderr ---\n%s", result.ExitCode, result.TimedOut, result.Stdout, result.Stderr)
}
