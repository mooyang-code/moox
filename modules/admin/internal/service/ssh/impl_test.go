package ssh

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/dao"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSSHTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return db
}

func newTestSSHService(t *testing.T) *ServiceImpl {
	return NewService(dao.NewHostDAO(setupSSHTestDB(t)))
}

func TestServiceImpl_CreateSession_UnregisteredHost_ShouldFail(t *testing.T) {
	svc := newTestSSHService(t)
	_, err := svc.CreateSession(context.Background(), "missing", "", "user-1")
	require.ErrorIs(t, err, dao.ErrHostNotFound)
}

func TestServiceImpl_ResizeWindow_MissingSession_ShouldError(t *testing.T) {
	svc := newTestSSHService(t)
	err := svc.ResizeWindow(context.Background(), "missing-session", 80, 24)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "会话不存在")
}

func TestServiceImpl_SftpList_MissingSession_ShouldError(t *testing.T) {
	svc := newTestSSHService(t)
	_, err := svc.SftpList(context.Background(), "missing", "/")
	require.Error(t, err)
}

func TestServiceImpl_DisconnectSession_UnknownSession_ShouldPass(t *testing.T) {
	svc := newTestSSHService(t)
	require.NoError(t, svc.DisconnectSession(context.Background(), "unknown"))
}

func TestParsePaths_AbsolutePath_ShouldBuildBreadcrumbs(t *testing.T) {
	paths := parsePaths("/var/log")
	require.Len(t, paths, 3)
	assert.Equal(t, "/", paths[0]["name"])
	assert.Equal(t, "var", paths[1]["name"])
	assert.Equal(t, "log", paths[2]["name"])
	assert.Equal(t, "/var/log", paths[2]["dir"])
}

func TestParsePaths_RelativePath_ShouldBuildBreadcrumbs(t *testing.T) {
	paths := parsePaths("tmp/data")
	require.Len(t, paths, 2)
	assert.Equal(t, "tmp", paths[0]["name"])
}

// CreateSession 连接真实 SSH 的部分在 conn 包里用本地 SSH 服务端测试。
