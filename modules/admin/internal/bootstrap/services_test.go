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

const testBootstrapEncryptionKey = "0123456789abcdef0123456789abcdef"

func TestStartBackgroundWorkers_EmptyServices_ShouldPass(t *testing.T) {
	require.NoError(t, startBackgroundWorkers(context.Background(), &Services{}))
}

func setupBootstrapTestDB(t *testing.T) *database.Manager {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	mgr := database.NewManager()
	require.NoError(t, mgr.Initialize(&config.DatabaseConfig{Path: dbPath}))
	require.NoError(t, mgr.GetDB().Exec(schema.AdminSQL()).Error)
	return mgr
}

func TestCreateCoreServices_ValidDB_ShouldCreateServices(t *testing.T) {
	mgr := setupBootstrapTestDB(t)

	cfg := &Config{App: &config.AppConfig{}, AdminNodeID: "admin-node-test", EncryptionKey: testBootstrapEncryptionKey}
	services, err := createCoreServices(context.Background(), mgr, cfg)
	require.NoError(t, err)
	require.NotNil(t, services)
	require.NotNil(t, services.SSHService)
	require.NotNil(t, services.SecretDAO)
	require.NotNil(t, services.SysDeploy)
	require.NotNil(t, services.ActivityLog)
}

func TestCreateCoreServices_MissingEncryptionKey_ShouldError(t *testing.T) {
	mgr := setupBootstrapTestDB(t)
	_, err := createCoreServices(context.Background(), mgr, &Config{App: &config.AppConfig{}, AdminNodeID: "admin-node-test"})
	require.Error(t, err)
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

	cfg := &Config{
		AdminNodeID:   "admin-node-test",
		EncryptionKey: testBootstrapEncryptionKey,
		App: &config.AppConfig{
			Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "unused.db")},
		},
	}
	services, err := createCoreServices(context.Background(), mgr, cfg)
	require.NoError(t, err)
	require.NoError(t, startBackgroundWorkers(context.Background(), services))
}

var _ = sqlite.Open
