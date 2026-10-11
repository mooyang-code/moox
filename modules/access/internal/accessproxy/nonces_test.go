package accessproxy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenSQLiteNoncesSecuresStorePath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	path := filepath.Join(dir, "nonces.db")

	store, err := OpenSQLiteNonces(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assertPerm := func(info os.FileInfo, mode os.FileMode) {
		require.Equal(t, mode, info.Mode().Perm())
	}
	assertPerm(dirInfo, 0o700)

	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	assertPerm(fileInfo, 0o600)
}

// 有效的 nonce 重放被拒绝；过期后同一个 nonce 可以安全复用；过期记录按批清理，不在每次请求里全部删除。
func TestSQLiteNoncesReplayExpiryAndBatchedPrune(t *testing.T) {
	nonces, err := OpenSQLiteNonces(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer nonces.Close()
	ctx := context.Background()
	if ok, err := nonces.Consume(ctx, "ns", "a", time.Hour); err != nil || !ok {
		t.Fatalf("首次使用应成功：%v %v", ok, err)
	}
	if ok, err := nonces.Consume(ctx, "ns", "a", time.Hour); err != nil || ok {
		t.Fatalf("有效期内的重放必须被拒绝：%v %v", ok, err)
	}
	if ok, _ := nonces.Consume(ctx, "other", "a", time.Hour); !ok {
		t.Fatal("不同命名空间互不影响")
	}
	if ok, err := nonces.Consume(ctx, "ns", "short", 30*time.Millisecond); err != nil || !ok {
		t.Fatal("短 TTL 首次使用应成功")
	}
	time.Sleep(60 * time.Millisecond)
	if ok, err := nonces.Consume(ctx, "ns", "short", time.Hour); err != nil || !ok {
		t.Fatalf("过期后同一个 nonce 可复用（不依赖清理是否执行）：%v %v", ok, err)
	}
	// 清理每次最多删一批。
	for i := 0; i < noncePruneBatch+50; i++ {
		if _, err := nonces.db.Exec(`INSERT INTO gateway_nonces(namespace, nonce, expires_at) VALUES('old', ?, 1)`, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	nonces.lastPrune.Store(0)
	if _, err := nonces.Consume(ctx, "ns", "trigger", time.Hour); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := nonces.db.QueryRow(`SELECT COUNT(*) FROM gateway_nonces WHERE namespace='old'`).Scan(&left); err != nil || left != 50 {
		t.Fatalf("一次清理最多删除 %d 条，剩余应为 50：%d %v", noncePruneBatch, left, err)
	}
}
