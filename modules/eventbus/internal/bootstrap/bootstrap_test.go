package bootstrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartOrdersBrokerRegistryAndHealth(t *testing.T) {
	raw, err := os.ReadFile("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	storeDir := filepath.Join(t.TempDir(), "jetstream")
	text := strings.ReplaceAll(string(raw), "port: 4222", "port: "+freePort(t))
	text = strings.ReplaceAll(text, "store_dir: ./data/eventbus/jetstream", "store_dir: "+storeDir)
	text = strings.ReplaceAll(text, "addr: \":11419\"", "addr: \"127.0.0.1:0\"")
	for _, maxBytes := range []string{
		"21474836480", "10737418240", "2147483648",
		"8589934592", "4294967296", "2147483648", "1073741824", "536870912", "268435456",
	} {
		text = strings.ReplaceAll(text, maxBytes, "1048576")
	}
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rt, err := Start(ctx, nil, path)
	if err != nil {
		t.Fatal(err)
	}
	if !rt.Broker.Ready() || !rt.Health.Ready() {
		t.Fatal("runtime did not become ready")
	}
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.Broker.Ready() {
		t.Fatal("broker remains ready after shutdown")
	}
}

func TestReadInternalCredentialsResolvesRelativeCAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "internal-admin.yaml")
	if err := os.WriteFile(path, []byte("username: eventbus-internal-admin\ntoken: secret\nca_file: ca.pem\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	credentials, err := readInternalCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.CAFile != filepath.Join(dir, "ca.pem") {
		t.Fatalf("CAFile = %q, want %q", credentials.CAFile, filepath.Join(dir, "ca.pem"))
	}
}

func TestInternalClientCAFilePrefersConfiguredDeploymentCA(t *testing.T) {
	if got := internalClientCAFile("/etc/moox/eventbus/ca.pem", "/tmp/credential-dir/ca.pem", "/etc/moox/broker-ca.pem"); got != "/etc/moox/eventbus/ca.pem" {
		t.Fatalf("configured CA = %q", got)
	}
	if got := internalClientCAFile("", "/tmp/credential-dir/ca.pem", "/etc/moox/broker-ca.pem"); got != "/tmp/credential-dir/ca.pem" {
		t.Fatalf("credential CA = %q", got)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
}
