package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

func TestRoutesSaveLoadAndRejectInvalidSnapshotWithoutReplacingFile(t *testing.T) {
	dir := t.TempDir()
	routes := NewRoutes(dir)
	valid, err := gatewayproxy.NormalizeAndHash("node-a", []gatewayproxy.Route{{ServiceID: "monitor", Address: "127.0.0.1:11410", ServicePath: "trpc.moox.monitor.MonitorMgr"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := routes.Save(valid); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	got, err := routes.Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got.RouteHash != valid.RouteHash {
		t.Fatalf("hash = %q", got.RouteHash)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "routes.json"))
	valid.RouteHash = "broken"
	if err := routes.Save(valid); err == nil {
		t.Fatal("Save() accepted bad hash")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "routes.json"))
	if string(after) != string(before) {
		t.Fatal("invalid save changed route file")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".routes-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

func TestRoutesLoadRejectsUnknownAndTrailingJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.json")
	for name, contents := range map[string]string{
		"unknown field":  `{"node_id":"node-a","unknown":true}`,
		"trailing value": "{}\n{}",
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewRoutes(dir).Load(); err == nil {
				t.Fatal("Load() accepted invalid JSON")
			}
		})
	}
}

func TestNonceConsumePersistsAndRejectsConcurrentDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonces")
	nonces, err := OpenNonces(path)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := nonces.Consume(context.Background(), "service", "abc", time.Minute)
			if err != nil {
				t.Errorf("Consume() = %v", err)
				return
			}
			if ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted = %d", accepted.Load())
	}
	if err := nonces.Close(); err != nil {
		t.Fatal(err)
	}
	nonces, err = OpenNonces(path)
	if err != nil {
		t.Fatal(err)
	}
	defer nonces.Close()
	if ok, err := nonces.Consume(context.Background(), "service", "abc", time.Minute); err != nil || ok {
		t.Fatalf("persistent consume = %v, %v", ok, err)
	}
}
