package auth

import (
	"path/filepath"
	"testing"

	appconfig "github.com/mooyang-code/moox/modules/admin/internal/config"
	authcfg "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewService_UninitializedDB_ShouldError(t *testing.T) {
	mgr := database.NewManager()
	_, err := NewService(&authcfg.Config{}, mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "数据库连接未初始化")
}

func TestNewService_ValidDBAndCache_ShouldReturnService(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	mgr := database.NewManager()
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	cacheDir := filepath.Join(t.TempDir(), "badger")
	require.NoError(t, mgr.Initialize(&appconfig.DatabaseConfig{Path: dbPath}))
	require.NoError(t, mgr.InitializeCache(cacheDir))
	defer func() {
		if mgr.GetCache() != nil {
			_ = mgr.GetCache().Close()
		}
	}()

	svc, err := NewService(&authcfg.Config{}, mgr)
	require.NoError(t, err)
	require.NotNil(t, svc)
}
