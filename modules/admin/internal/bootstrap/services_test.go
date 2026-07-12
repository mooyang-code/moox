package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
)

func TestStartBackgroundWorkers_EmptyServices_ShouldPass(t *testing.T) {
	require.NoError(t, startBackgroundWorkers(context.Background(), &Services{}))
}

func setupBootstrapTestDB(t *testing.T) *database.Manager {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	mgr := database.NewManager()
	require.NoError(t, mgr.Initialize(&config.DatabaseConfig{Path: dbPath}))
	require.NoError(t, mgr.GetDB().Exec(schema.AdminSQL()).Error)
	return mgr
}

func TestCreateCoreServices_ValidDB_ShouldCreateServices(t *testing.T) {
	mgr := setupBootstrapTestDB(t)
	defer func() {
		if mgr.GetCache() != nil {
			_ = mgr.GetCache().Close()
		}
	}()

	cfg := &Config{App: &config.AppConfig{}}
	services, err := createCoreServices(context.Background(), mgr, cfg)
	require.NoError(t, err)
	require.NotNil(t, services)
	require.NotNil(t, services.SpaceMgr)
	require.NotNil(t, services.SSHService)
	require.NotNil(t, services.SecretService)
	require.NotNil(t, services.SysDeploy)
}

func TestInitializeDatabase_ValidPath_ShouldOpenDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	mgr, err := initializeDatabase(&config.DatabaseConfig{Path: dbPath})
	require.NoError(t, err)
	require.NotNil(t, mgr)
	require.NotNil(t, mgr.GetDB())
}

func TestStartBackgroundServices_FullFlow_ShouldPass(t *testing.T) {
	mgr := setupBootstrapTestDB(t)
	defer func() {
		if mgr.GetCache() != nil {
			_ = mgr.GetCache().Close()
		}
	}()

	cfg := &Config{
		App: &config.AppConfig{
			Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "unused.db")},
		},
	}
	// 复用已初始化的 DB manager，避免重复打开同一文件。
	services, err := createCoreServices(context.Background(), mgr, cfg)
	require.NoError(t, err)
	require.NoError(t, startBackgroundWorkers(context.Background(), services))
}

// 确保 sqlite 驱动在测试中可用。
var _ = sqlite.Open
