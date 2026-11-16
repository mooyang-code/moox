// Package testkit runs the production Access binary against the native host
// gateway test router. It is used only by cross-module integration tests.
//
// T15～T16 之间的过渡：Access 与 storage-primary 部署在同一台主机上，网关客户端对同机服务直连
// 127.0.0.1 加服务目录里的服务端口，测试缝（SetLocalServiceAddressForTest）进不了 Access 这个子进程。
// 所以夹具网关只给 Access 提供目录，测试里的假 Storage 必须监听目录里那个固定的回环端口（LocalStorageListener）。
// T16 删掉 Access、把夹具改成带外部入口的网关之后，这里的固定端口随之去掉。
package testkit

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type AccessOptions struct {
	AccessBinary, GatewayBinary string
	HostID                      string
	// StorageAddress 是假 Storage 的地址，必须来自 LocalStorageListener：Access 直连的是目录里的固定端口，
	// 夹具网关只把它登记成本机 storage-primary 的上游（Access 的调用不经过夹具网关）。
	StorageAddress      string
	ExternalCredentials []gatewayauth.Credentials
	// SCF publication forbids loopback endpoints. Use a local interface in its
	// runtime integration tests, while keeping the internal gateway on loopback.
	SCF bool
}

type Access struct {
	Address, InstanceID string
}

// LocalStorageListener 监听 Access 直连本机 Storage 用的回环端口：127.0.0.1 加服务目录里 PrimaryStore 的服务端口。
// 端口是固定的，被别的进程占用（例如本机正跑着真实的 Storage）时跳过测试。
func LocalStorageListener(t *testing.T) net.Listener {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	service, ok := catalog.Service(storagePrimaryService)
	require.True(t, ok)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(service.Port))
	listener, err := net.Listen("tcp", address)
	if errors.Is(err, syscall.EADDRINUSE) {
		t.Skipf("跳过：Access 与 Storage 同机时直连固定端口 %s，测试里的假 Storage 必须监听它，但它已被别的进程占用（T16 改成带外部入口的网关后不再需要固定端口）", address)
	}
	require.NoError(t, err)
	return listener
}

const storagePrimaryService = "trpc.moox.storage.PrimaryStore"

func StartAccess(t *testing.T, options AccessOptions) Access {
	t.Helper()
	root := t.TempDir()
	// access 和别的组件一样用所在主机的密钥签名。
	hostKey, err := gatewayauth.GenerateSigningKey()
	require.NoError(t, err)
	internal := gatewayauth.Credentials{Caller: "access", Host: options.HostID, Key: hostKey}
	gatewayReady := filepath.Join(root, "gateway.ready")
	gateway := start(t, root, options.GatewayBinary, append(os.Environ(), "MOOX_GATEWAY_E2E_PUBLIC_KEY="+gatewayauth.EncodePublicKey(internal.PublicKey())),
		"-mode=access-native", "-node-id="+options.HostID, "-upstream-addr="+options.StorageAddress,
		"-ready-file="+gatewayReady)
	var gatewayAddress string
	wait(t, gateway, func() bool {
		value, err := os.ReadFile(gatewayReady)
		if err == nil {
			gatewayAddress = strings.TrimPrefix(strings.TrimSpace(string(value)), "ip://")
		}
		return err == nil && gatewayAddress != ""
	})
	caPath := filepath.Join(root, "ca.crt")
	writeCA(t, caPath)
	write(t, filepath.Join(root, "secrets/host.key"), []byte(gatewayauth.EncodeSigningKey(hostKey)+"\n"))
	entries := make([]map[string]string, 0, len(options.ExternalCredentials))
	for _, credential := range options.ExternalCredentials {
		entries = append(entries, map[string]string{"identity": credential.Caller, "public_key": gatewayauth.EncodePublicKey(credential.PublicKey())})
	}
	registry, err := json.Marshal(map[string]any{"version": 2, "identities": entries})
	require.NoError(t, err)
	write(t, filepath.Join(root, "secrets/verification.json"), registry)
	app := map[string]any{
		"gateway_client": map[string]string{
			"caller": "access", "key_file": "../../secrets/host.key",
			"host_id": options.HostID, "local_address": gatewayAddress, "ca_file": caPath,
		},
		"verification_file": "../../secrets/verification.json", "nonce_path": "../../data/access/nonces.db",
	}
	appBytes, err := yaml.Marshal(app)
	require.NoError(t, err)
	appPath := filepath.Join(root, "access/config/app.yaml")
	write(t, appPath, appBytes)
	ip := "127.0.0.1"
	if options.SCF {
		ip = localInterface(t)
	}
	address, port := reserve(t, ip)
	healthAddress, healthPort := reserve(t, "127.0.0.1")
	_, adminPort := reserve(t, "127.0.0.1")
	framework := fmt.Sprintf(`global:
  namespace: Development
  env_name: access-e2e
server:
  filter: []
  timeout: 120000
  admin:
    ip: 127.0.0.1
    port: %s
  service:
    - name: trpc.moox.access.Access
      ip: %s
      port: %s
      network: tcp
      protocol: trpc
      transport: go-net
      timeout: 120000
    - name: trpc.moox.access.Health
      ip: 127.0.0.1
      port: %s
      network: tcp
      protocol: http_no_protocol
      timeout: 3000
`, adminPort, ip, port, healthPort)
	frameworkPath := filepath.Join(root, "access/config/trpc_go.yaml")
	write(t, frameworkPath, []byte(framework))
	process := start(t, root, options.AccessBinary, os.Environ(), "-config="+appPath, "-conf="+frameworkPath)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	wait(t, process, func() bool {
		response, err := client.Get("http://" + healthAddress + "/healthz")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
	return Access{Address: address, InstanceID: "access@" + options.HostID}
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func writeCA(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	write(t, path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func reserve(t *testing.T, ip string) (string, string) {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	_, port, err := net.SplitHostPort(address)
	require.NoError(t, err)
	return address, port
}

func localInterface(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	require.NoError(t, err)
	for _, address := range addresses {
		if ip, ok := address.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLoopback() && !ip.IP.IsUnspecified() {
			return ip.IP.String()
		}
	}
	t.Fatal("SCF integration requires a local IPv4 interface")
	return ""
}

type process struct {
	command *exec.Cmd
	mu      sync.Mutex
	output  bytes.Buffer
	done    chan struct{}
	err     error
}

func (p *process) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.output.Write(data)
}

func start(t *testing.T, root, binary string, environment []string, args ...string) *process {
	t.Helper()
	p := &process{command: exec.Command(binary, args...), done: make(chan struct{})}
	p.command.Dir, p.command.Env = root, environment
	p.command.Stdout, p.command.Stderr = p, p
	require.NoError(t, p.command.Start())
	go func() { p.err = p.command.Wait(); close(p.done) }()
	t.Cleanup(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = p.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
			require.NoError(t, p.err, "fixture process did not shut down cleanly")
		case <-time.After(5 * time.Second):
			_ = p.command.Process.Kill()
			<-p.done
			t.Error("fixture process shutdown timed out")
		}
	})
	return p
}

func wait(t *testing.T, p *process, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		if ready() {
			return
		}
		select {
		case <-p.done:
			p.mu.Lock()
			defer p.mu.Unlock()
			t.Fatalf("fixture exited: %v\n%s", p.err, p.output.String())
		case <-ctx.Done():
			p.mu.Lock()
			defer p.mu.Unlock()
			t.Fatalf("fixture readiness timeout: %s", p.output.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
