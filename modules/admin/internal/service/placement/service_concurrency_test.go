package placement

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 生产的 SQLite 连接池有多条连接、事务先读后写：没有 _txlock=immediate 时，并发写会在升级写锁时
// 返回 SQLITE_BUSY_SNAPSHOT。这里用与生产一致的 DSN 和连接数并发调用写方法，要求全部成功。
func TestConcurrentWritesDoNotFailWithBusySnapshot(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "admin.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_txlock=immediate"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	s := NewService(db, nil)
	syncProduction(t, s)

	const rounds = 30
	errCh := make(chan error, rounds*4)
	var wg sync.WaitGroup
	run := func(call func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- call()
		}()
	}
	ctx := context.Background()
	for i := 0; i < rounds; i++ {
		i := i
		run(func() error {
			_, err := s.RecordGatewayReport(ctx, GatewayReport{
				HostID: "storage", InstanceID: fmt.Sprintf("instance-%d", i%2), Version: "test", AppliedHash: "h",
			})
			return err
		})
		run(func() error { return s.SetExpectedHash(ctx, "storage", fmt.Sprintf("hash-%d", i)) })
		run(func() error {
			_, err := s.SyncHostPlacements(ctx, productionHosts["compute-1"], productionPlacements["compute-1"])
			return err
		})
		run(func() error {
			_, err := s.SetPlacementStatus(ctx, "storage", "storage-view", "enabled")
			return err
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}
