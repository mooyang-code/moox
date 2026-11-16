package auth

import (
	"path/filepath"
	"testing"
	"time"

	appconfig "github.com/mooyang-code/moox/modules/admin/internal/config"
	authcfg "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewService_UninitializedDB_ShouldError(t *testing.T) {
	sessions, err := session.NewStore(time.Hour)
	require.NoError(t, err)
	_, err = NewService(&authcfg.Config{}, database.NewManager(), sessions)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "数据库连接未初始化")
}

func TestNewService_ValidDB_ShouldReturnService(t *testing.T) {
	mgr := database.NewManager()
	require.NoError(t, mgr.Initialize(&appconfig.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}))
	sessions, err := session.NewStore(time.Hour)
	require.NoError(t, err)

	svc, err := NewService(&authcfg.Config{}, mgr, sessions)
	require.NoError(t, err)
	require.NotNil(t, svc)
	_, err = NewService(&authcfg.Config{}, mgr, nil)
	require.Error(t, err, "没有会话表时拒绝创建")
}
