package test

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

// buildAccessBinary 编译外部接入 moox-access。
func buildAccessBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "moox-access")
	build := exec.Command("go", "build", "-o", binary, "./cmd/server")
	build.Dir = filepath.Join("..", "..", "access")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "编译 moox-access: %s", output)
	return binary
}

// startAccessProcess 在临时安装目录中启动 moox-access：以 access 身份经 hostGatewayAddress（e2e-helper 的本机入口）
// 转发外部调用方的请求，principals 是登记在外部接入上的外部调用方密钥。返回外部接入的入站地址。
func startAccessProcess(t *testing.T, binary, hostGatewayAddress string, accessKey gatewayauth.CallerKey, principals ...gatewayauth.CallerKey) string {
	t.Helper()
	root := t.TempDir()
	componentDir := filepath.Join(root, "access")
	for _, dir := range []string{filepath.Join(root, "secrets"), filepath.Join(root, "certs"), filepath.Join(componentDir, "config")} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	callerKey, err := gatewayauth.MarshalCallerKey(accessKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "secrets", "caller-access.key"), callerKey, 0o600))
	keySet, err := gatewayauth.MarshalKeySet(principals)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "secrets", "access-principals.json"), keySet, 0o600))
	// 本机转发不走 TLS，CA 文件只需要存在于配置中。
	require.NoError(t, os.WriteFile(filepath.Join(root, "certs", "moox-ca.crt"), []byte("unused"), 0o600))

	accessPort, healthPort := freeLoopbackPort(t), freeLoopbackPort(t)
	appConfig := fmt.Sprintf(`gateway_client:
  mode: local
  caller: access
  key_file: ../secrets/caller-access.key
  ca_file: ../certs/moox-ca.crt
  cache_dir: ./data/gatewayclient
  local_address: %s
principals_file: ../secrets/access-principals.json
nonce_path: ./data/nonces.db
max_body_bytes: 1048576
timeout: 10s
`, hostGatewayAddress)
	require.NoError(t, os.WriteFile(filepath.Join(componentDir, "config", "app.yaml"), []byte(appConfig), 0o600))
	trpcConfig := fmt.Sprintf(`global:
  namespace: Development
  env_name: access-e2e
server:
  service:
    - name: trpc.moox.access.Access
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
      timeout: 10000
    - name: trpc.moox.access.Health
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: http_no_protocol
      timeout: 3000
`, accessPort, healthPort)
	require.NoError(t, os.WriteFile(filepath.Join(componentDir, "config", "trpc_go.yaml"), []byte(trpcConfig), 0o600))

	var logs bytes.Buffer
	command := exec.Command(binary, "-conf=config/trpc_go.yaml")
	command.Dir = componentDir
	command.Env = append(os.Environ(), "MOOX_HEALTH_AUTH_ACCESS_KEY=access-e2e", "MOOX_HEALTH_AUTH_SECRET_KEY=access-e2e-health-secret")
	command.Stdout = &logs
	command.Stderr = &logs
	require.NoError(t, command.Start())
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	t.Cleanup(func() {
		select {
		case <-exited:
			return
		default:
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-exited
		}
	})

	address := net.JoinHostPort("127.0.0.1", fmt.Sprint(accessPort))
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-exited:
			t.Fatalf("moox-access 提前退出: %v\n%s", err, logs.String())
		default:
		}
		if conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			return address
		}
		if time.Now().After(deadline) {
			t.Fatalf("moox-access 没有在 30 秒内监听 %s: %s", address, strings.TrimSpace(logs.String()))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
