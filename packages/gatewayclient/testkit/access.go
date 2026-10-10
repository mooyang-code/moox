// Package testkit runs the production Access binary against the native host
// gateway test router. It is used only by cross-module integration tests.
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
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type AccessOptions struct {
	AccessBinary, GatewayBinary string
	HostID                      string
	StorageAddress              string
	RuntimeAddress              string
	ExternalCredentials         []gatewayauth.Credentials
	// SCF publication forbids loopback endpoints. Use a local interface in its
	// runtime integration tests, while keeping the internal gateway on loopback.
	SCF bool
}

type Access struct {
	Address, InstanceID string
}

func StartAccess(t *testing.T, options AccessOptions) Access {
	t.Helper()
	root := t.TempDir()
	internal := gatewayauth.Credentials{Caller: "access", KeyID: "assigned-internal-access-85", Secret: "fixture-internal-access-signing-key-at-least-32-bytes"}
	gatewayReady := filepath.Join(root, "gateway.ready")
	gateway := start(t, root, options.GatewayBinary, append(os.Environ(), "MOOX_GATEWAY_E2E_SERVICE_SECRET="+internal.Secret),
		"-mode=access-native", "-node-id="+options.HostID, "-upstream-addr="+options.StorageAddress,
		"-runtime-upstream-addr="+options.RuntimeAddress, "-ready-file="+gatewayReady,
		"-nonce-dir="+filepath.Join(root, "gateway-nonces"), "-key-id="+internal.KeyID)
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
	host := hostgatewayconfig.Default(options.HostID, "fixture-control", "192.0.2.1", "assigned-host-key-12")
	host.Server.LocalAddr = gatewayAddress
	host.TLS.CAFile = caPath
	hostBytes, err := hostgatewayconfig.Encode(host)
	require.NoError(t, err)
	write(t, filepath.Join(root, "host-gateway/config/app.yaml"), hostBytes)
	write(t, filepath.Join(root, "secrets/access.key"), []byte(internal.Secret))
	entries := make([]map[string]string, 0, len(options.ExternalCredentials))
	for index, credential := range options.ExternalCredentials {
		file := fmt.Sprintf("external-%d.key", index)
		write(t, filepath.Join(root, "secrets", file), []byte(credential.Secret))
		entries = append(entries, map[string]string{"caller": credential.Caller, "key_id": credential.KeyID, "secret_file": file})
	}
	registry, err := json.Marshal(map[string]any{"version": 1, "credentials": entries})
	require.NoError(t, err)
	write(t, filepath.Join(root, "secrets/verification.json"), registry)
	app := map[string]any{
		"gateway_client":    map[string]string{"caller": "access", "key_id": internal.KeyID, "key_file": "../../secrets/access.key"},
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
	const healthKey = "fixture-health-signing-key-at-least-32-bytes"
	process := start(t, root, options.AccessBinary, append(os.Environ(), "MOOX_HEALTH_AUTH_VERSION=moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY=fixture-monitor", "MOOX_HEALTH_AUTH_SECRET_KEY="+healthKey), "-config="+appPath, "-conf="+frameworkPath)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	wait(t, process, func() bool {
		request, err := http.NewRequest(http.MethodGet, "http://"+healthAddress+"/healthz", nil)
		require.NoError(t, err)
		now := time.Now()
		nonce, err := requestauth.NewNonce()
		require.NoError(t, err)
		signature, err := requestauth.Sign(healthKey, requestauth.Material{Method: request.Method, Path: request.URL.EscapedPath(), Timestamp: now.Unix(), Nonce: nonce})
		require.NoError(t, err)
		request.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("moox-health-v1/fixture-monitor/%d/%s/%s", now.Unix(), nonce, signature))
		response, err := client.Do(request)
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
