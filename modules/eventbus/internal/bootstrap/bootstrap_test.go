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
	text = strings.ReplaceAll(text, "21474836480", "1048576")
	text = strings.ReplaceAll(text, "10737418240", "1048576")
	text = strings.ReplaceAll(text, "2147483648", "1048576")
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

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
}
