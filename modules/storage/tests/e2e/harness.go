//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

// e2e 服务端口。刻意避开默认 config/trpc_go.yaml 的 18xxx 端口段，
// 这样本地即使已经跑着一套默认部署也不会冲突。
const (
	portPrimary      = 28101
	portQuery        = 28202
	portMetadataHTTP = 29101
	portDataHTTP     = 29104
	portAdmin        = 29000
)

// e2eSpaceID 是固定的 e2e 业务空间 ID。
// archive timer 的 params 在部署期就写死了 space_id，所以这里必须保持一致。
const e2eSpaceID = "crypto"

// callTimeout 是单次 tRPC 调用超时。
const callTimeout = 15 * time.Second

// Harness 负责在本地把 storage 整套子服务真实部署起来（独立进程 + 独立目录 + 独立端口），
// 并对外提供 HTTP/tRPC 客户端，供各模块端到端测试驱动。
type Harness struct {
	moduleDir  string
	workDir    string
	binPath    string
	configPth  string
	storageCfg string
	logPath    string

	cmd     *exec.Cmd
	logFile *os.File
}

// NewHarness 创建一个 e2e 部署环境（尚未启动）。
func NewHarness() (*Harness, error) {
	moduleDir, err := locateModuleDir()
	if err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp("", "moox-storage-e2e-")
	if err != nil {
		return nil, fmt.Errorf("创建 e2e 工作目录失败: %w", err)
	}
	return &Harness{
		moduleDir:  moduleDir,
		workDir:    workDir,
		binPath:    filepath.Join(workDir, "moox-storage"),
		configPth:  filepath.Join(workDir, "trpc_go.yaml"),
		storageCfg: filepath.Join(workDir, "storage.yaml"),
		logPath:    filepath.Join(workDir, "server.log"),
	}, nil
}

// Start 编译二进制、初始化元数据 schema、拉起服务并等待端口就绪。
func (h *Harness) Start(ctx context.Context) error {
	if err := h.build(ctx); err != nil {
		return err
	}
	if err := h.writeConfig(); err != nil {
		return err
	}
	if err := h.initMetadata(ctx); err != nil {
		return err
	}
	if err := h.launch(); err != nil {
		return err
	}
	if err := h.waitReady(60 * time.Second); err != nil {
		_ = h.Stop()
		return fmt.Errorf("等待服务就绪失败: %w\n----- server.log -----\n%s", err, h.tailLog())
	}
	return nil
}

// Stop 优雅停止服务并清理工作目录。
func (h *Harness) Stop() error {
	if err := h.StopProcess(); err != nil {
		return err
	}
	if h.workDir != "" {
		_ = os.RemoveAll(h.workDir)
	}
	return nil
}

// StopProcess 优雅停止服务进程但保留工作目录，供测试直接检查底层存储文件。
func (h *Harness) StopProcess() error {
	var firstErr error
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() {
			_, err := h.cmd.Process.Wait()
			done <- err
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			if err := h.cmd.Process.Kill(); err != nil && firstErr == nil {
				firstErr = err
			}
			<-done
		}
		h.cmd = nil
	}
	if h.logFile != nil {
		if err := h.logFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		h.logFile = nil
	}
	return firstErr
}

// WorkDir 返回 e2e 工作目录（部署目录），方便排障时查看产物。
func (h *Harness) WorkDir() string { return h.workDir }

// LogTail 返回服务端日志末尾，用于测试失败时打印上下文。
func (h *Harness) LogTail() string { return h.tailLog() }

func (h *Harness) build(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", h.binPath, "./cmd/moox-storage")
	cmd.Dir = h.moduleDir
	// CGO 必须开启，DuckDB 视图存储依赖 //go:build cgo 实现，否则会退化为 fallback。
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("编译 moox-storage 失败: %w\n%s", err, out)
	}
	return nil
}

func (h *Harness) initMetadata(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, h.binPath, "-conf="+h.configPth, "-init-metadata")
	cmd.Dir = h.workDir
	cmd.Env = h.childEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("初始化元数据 schema 失败: %w\n%s", err, out)
	}
	return nil
}

func (h *Harness) launch() error {
	logFile, err := os.Create(h.logPath)
	if err != nil {
		return fmt.Errorf("创建日志文件失败: %w", err)
	}
	h.logFile = logFile

	cmd := exec.Command(h.binPath, "-conf="+h.configPth)
	cmd.Dir = h.workDir
	cmd.Env = h.childEnv()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 moox-storage 失败: %w", err)
	}
	h.cmd = cmd
	return nil
}

func (h *Harness) childEnv() []string {
	env := os.Environ()
	// 显式指定 schema 文件绝对路径，避免依赖 cwd 推断。
	env = append(env, "STORAGE_SCHEMA_FILE="+filepath.Join(h.moduleDir, "schema", "metadata.sql"))
	env = append(env, "MOOX_STORAGE_CONFIG="+h.storageCfg)
	return env
}

func (h *Harness) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ports := []int{portPrimary, portQuery, portMetadataHTTP, portDataHTTP}
	for time.Now().Before(deadline) {
		if h.exited() {
			return fmt.Errorf("服务进程提前退出")
		}
		if missing := missingOpenPorts(ports); len(missing) == 0 {
			// 端口已监听，再多等一拍让服务完成内部初始化。
			time.Sleep(500 * time.Millisecond)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("端口在 %s 内未全部就绪，未监听端口: %v", timeout, missingOpenPorts(ports))
}

func (h *Harness) exited() bool {
	if h.cmd == nil || h.cmd.ProcessState != nil {
		return h.cmd != nil && h.cmd.ProcessState != nil
	}
	return false
}

func (h *Harness) tailLog() string {
	data, err := os.ReadFile(h.logPath)
	if err != nil {
		return ""
	}
	const max = 8000
	if len(data) > max {
		data = data[len(data)-max:]
	}
	return string(data)
}

// ---- HTTP/tRPC 客户端 ----

func (h *Harness) MetadataClient() pb.MetadataServiceClientProxy {
	return pb.NewMetadataServiceClientProxy(httpTargetOpts(portMetadataHTTP)...)
}

func (h *Harness) DataClient() pb.AccessServiceClientProxy {
	return pb.NewAccessServiceClientProxy(httpTargetOpts(portDataHTTP)...)
}

func (h *Harness) QueryClient() pb.ViewServiceClientProxy {
	return pb.NewViewServiceClientProxy(targetOpts(portQuery)...)
}

func (h *Harness) PrimaryClient() pb.PrimaryStoreServiceClientProxy {
	return pb.NewPrimaryStoreServiceClientProxy(targetOpts(portPrimary)...)
}

func targetOpts(port int) []client.Option {
	return []client.Option{
		client.WithTarget(fmt.Sprintf("ip://127.0.0.1:%d", port)),
		client.WithProtocol("trpc"),
		client.WithNetwork("tcp"),
		client.WithTimeout(callTimeout),
	}
}

func httpTargetOpts(port int) []client.Option {
	return []client.Option{
		client.WithTarget(fmt.Sprintf("ip://127.0.0.1:%d", port)),
		client.WithProtocol("http"),
		client.WithNetwork("tcp"),
		client.WithTimeout(callTimeout),
	}
}

// ---- 辅助函数 ----

func allPortsOpen(ports []int) bool {
	return len(missingOpenPorts(ports)) == 0
}

func missingOpenPorts(ports []int) []int {
	var missing []int
	for _, port := range ports {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err != nil {
			missing = append(missing, port)
			continue
		}
		_ = conn.Close()
	}
	return missing
}

// locateModuleDir 通过本文件位置定位 modules/storage 目录。
func locateModuleDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("无法定位 harness.go 路径")
	}
	// file = <moduleDir>/tests/e2e/harness.go
	moduleDir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(moduleDir, "go.mod")); err != nil {
		return "", fmt.Errorf("定位 module 目录失败（%s 下无 go.mod）: %w", moduleDir, err)
	}
	return moduleDir, nil
}
