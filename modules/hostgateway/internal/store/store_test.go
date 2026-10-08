package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"google.golang.org/protobuf/proto"
)

func secureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "host-gateway")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSnapshotsSaveAndLoad(t *testing.T) {
	dir := secureDir(t)
	snapshots := NewSnapshots(dir)
	if _, err := snapshots.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没有缓存时应当返回 ErrNotExist，实际 %v", err)
	}
	want := &adminpb.HostSnapshot{HostId: "storage", Hash: "h", Routes: []*adminpb.HostRoute{{ServicePath: "trpc.moox.storage.DataView", Methods: []string{"QueryTimeSeriesRows"}}},
		Keys: []*adminpb.VerificationKey{{KeyId: "strategy-1", Caller: "strategy", Secret: "s"}}}
	if err := snapshots.Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(snapshots.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("快照缓存应当是 0600: %v %v", info, err)
	}
	got, err := snapshots.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(want, got) {
		t.Fatalf("got %v", got)
	}
	if err := snapshots.Check(); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".snapshot-*.tmp")); len(matches) != 0 {
		t.Fatalf("留下了临时文件: %v", matches)
	}
}

func TestSnapshotsRejectInsecureCache(t *testing.T) {
	dir := secureDir(t)
	snapshots := NewSnapshots(dir)
	if err := snapshots.Save(&adminpb.HostSnapshot{HostId: "storage"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(snapshots.Path(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.Load(); err == nil {
		t.Fatal("组可读的缓存应当被拒绝")
	}
	_ = os.Chmod(snapshots.Path(), 0o600)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := snapshots.Check(); err == nil {
		t.Fatal("组可读的目录应当被拒绝")
	}
	_ = os.Chmod(dir, 0o700)
	if err := os.Remove(snapshots.Path()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, snapshots.Path()); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.Load(); err == nil {
		t.Fatal("符号链接应当被拒绝")
	}
	if err := os.Remove(snapshots.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshots.Path(), []byte(`{"hostId":"storage","unknown":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshots.Load(); err == nil {
		t.Fatal("未知字段应当被拒绝")
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
	if err := nonces.Check(); err != nil {
		t.Fatalf("nonces Check() = %v", err)
	}
}
