package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type serviceEnv struct {
	workDir    string
	binPath    string
	cliPath    string
	configPath string
	storageCfg string
	logPath    string
	ports      servicePorts
	cmd        *exec.Cmd
	logFile    *os.File
}

func newServiceEnv(opts options) (*serviceEnv, error) {
	ports, err := allocatePorts(8)
	if err != nil {
		return nil, err
	}
	workDir := filepath.Join(opts.workDir, "storage")
	return &serviceEnv{
		workDir:    workDir,
		binPath:    filepath.Join(workDir, "moox-storage"),
		cliPath:    filepath.Join(workDir, "moox-storage-cli"),
		configPath: filepath.Join(workDir, "trpc_go.yaml"),
		storageCfg: filepath.Join(workDir, "storage.yaml"),
		logPath:    filepath.Join(workDir, "server.log"),
		ports: servicePorts{
			admin: ports[0], data: ports[1], scan: ports[2], metadata: ports[3], query: ports[4], primary: ports[5], index: ports[6], timer: ports[7],
		},
	}, nil
}

func (e *serviceEnv) Start(ctx context.Context, moduleDir string) error {
	if err := os.MkdirAll(e.workDir, 0o755); err != nil {
		return err
	}
	if err := e.writeConfig(); err != nil {
		return err
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", e.binPath, "./cmd/server")
	build.Dir = moduleDir
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build moox-storage failed: %w\n%s", err, out)
	}
	buildCLI := exec.CommandContext(ctx, "go", "build", "-o", e.cliPath, "./cmd/cli")
	buildCLI.Dir = moduleDir
	buildCLI.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := buildCLI.CombinedOutput(); err != nil {
		return fmt.Errorf("build moox-storage-cli failed: %w\n%s", err, out)
	}
	initCmd := exec.CommandContext(ctx, e.cliPath, "init", "--storage-conf="+e.storageCfg, "--schema-path="+filepath.Join(moduleDir, "schema", "metadata.sql"))
	initCmd.Dir = e.workDir
	initCmd.Env = e.childEnv(moduleDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("init metadata failed: %w\n%s", err, out)
	}
	logFile, err := os.Create(e.logPath)
	if err != nil {
		return err
	}
	e.logFile = logFile
	cmd := exec.Command(e.binPath, "-conf="+e.configPath)
	cmd.Dir = e.workDir
	cmd.Env = e.childEnv(moduleDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	e.cmd = cmd
	if err := waitPorts([]int{e.ports.data, e.ports.scan, e.ports.metadata, e.ports.query, e.ports.primary, e.ports.index}, time.Minute); err != nil {
		_ = e.Stop()
		return fmt.Errorf("%w\n----- server.log -----\n%s", err, e.tailLog())
	}
	time.Sleep(500 * time.Millisecond)
	return nil
}

func (e *serviceEnv) Stop() error {
	var first error
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() {
			_, err := e.cmd.Process.Wait()
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, os.ErrProcessDone) {
				first = err
			}
		case <-time.After(5 * time.Second):
			if err := e.cmd.Process.Kill(); err != nil {
				first = err
			}
			<-done
		}
	}
	if e.logFile != nil {
		if err := e.logFile.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (e *serviceEnv) childEnv(moduleDir string) []string {
	return append(os.Environ(),
		"STORAGE_SCHEMA_FILE="+filepath.Join(moduleDir, "schema", "metadata.sql"),
		"MOOX_STORAGE_CONFIG="+e.storageCfg,
	)
}

func (e *serviceEnv) writeConfig() error {
	storageRoot := filepath.Join(e.workDir, "var", "storage")
	if err := os.MkdirAll(storageRoot, 0o755); err != nil {
		return err
	}
	storageCfg := fmt.Sprintf(storageConfigTemplate,
		storageRoot,
		filepath.Join(storageRoot, "metadata", "metadata.db"),
		filepath.Join(storageRoot, "pebble"),
		filepath.Join(storageRoot, "view-indexes"),
		filepath.Join(storageRoot, "archive"),
	)
	if err := os.WriteFile(e.storageCfg, []byte(storageCfg), 0o644); err != nil {
		return err
	}
	trpcCfg := fmt.Sprintf(trpcConfigTemplate,
		e.ports.admin, e.ports.data, e.ports.scan, e.ports.query, e.ports.primary, e.ports.metadata, e.ports.index, e.ports.timer,
	)
	return os.WriteFile(e.configPath, []byte(trpcCfg), 0o644)
}

func (e *serviceEnv) tailLog() string {
	data, err := os.ReadFile(e.logPath)
	if err != nil {
		return ""
	}
	if len(data) > 8000 {
		data = data[len(data)-8000:]
	}
	return string(data)
}
