package ssh

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
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
	db := setupSSHTestDB(t)
	return NewService(dao.NewSSHHostDAO(db), dao.NewSSHSessionDAO(db))
}

func TestServiceImpl_CreateHost_ValidHost_ShouldPersist(t *testing.T) {
	svc := newTestSSHService(t)
	host := &model.SSHHost{Name: "dev", Address: "10.0.0.1", Port: 22, User: "root", Password: "pwd"}
	require.NoError(t, svc.CreateHost(context.Background(), host))
	assert.NotZero(t, host.ID)
}

func TestServiceImpl_ListHosts_WithKeyword_ShouldSearch(t *testing.T) {
	svc := newTestSSHService(t)
	require.NoError(t, svc.CreateHost(context.Background(), &model.SSHHost{
		Name: "prod-web", Address: "10.0.0.2", Port: 22, User: "ops",
	}))
	hosts, total, err := svc.ListHosts(context.Background(), "prod", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, hosts, 1)
}

func TestServiceImpl_GetHost_NotFound_ShouldError(t *testing.T) {
	svc := newTestSSHService(t)
	_, err := svc.GetHost(context.Background(), 999)
	require.Error(t, err)
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

// CreateSession 依赖真实 SSH 连接，无法在单测环境稳定执行，见 ssh_conn_test.go 说明。
