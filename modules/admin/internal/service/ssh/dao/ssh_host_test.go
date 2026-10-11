package dao

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSSHDAOTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return db
}

func TestSSHHostDAO_Create_FindByID_RoundTrip_ShouldDecryptPassword(t *testing.T) {
	d := NewSSHHostDAO(setupSSHDAOTestDB(t))
	host := &model.SSHHost{
		Name: "dev", Address: "10.0.0.1", Port: 22, User: "root", Password: "plain-pwd",
	}
	require.NoError(t, d.Create(host))
	assert.NotZero(t, host.ID)

	got, err := d.FindByID(host.ID)
	require.NoError(t, err)
	assert.Equal(t, "plain-pwd", got.Password)
}

func TestSSHHostDAO_List_ReturnsCreatedHosts(t *testing.T) {
	d := NewSSHHostDAO(setupSSHDAOTestDB(t))
	require.NoError(t, d.Create(&model.SSHHost{Name: "a", Address: "1.1.1.1", Port: 22, User: "u"}))
	hosts, total, err := d.List(0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, hosts, 1)
}

func TestSSHHostDAO_Search_ByKeyword_ShouldMatch(t *testing.T) {
	d := NewSSHHostDAO(setupSSHDAOTestDB(t))
	require.NoError(t, d.Create(&model.SSHHost{Name: "prod-api", Address: "2.2.2.2", Port: 22, User: "u"}))
	hosts, total, err := d.Search("prod", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, hosts, 1)
}

func TestSSHHostDAO_Delete_ExistingHost_ShouldRemove(t *testing.T) {
	d := NewSSHHostDAO(setupSSHDAOTestDB(t))
	host := &model.SSHHost{Name: "tmp", Address: "3.3.3.3", Port: 22, User: "u"}
	require.NoError(t, d.Create(host))
	require.NoError(t, d.Delete(host.ID))
	_, err := d.FindByID(host.ID)
	require.Error(t, err)
}

func TestSSHSessionDAO_Create_FindBySessionID_RoundTrip_ShouldWork(t *testing.T) {
	d := NewSSHSessionDAO(setupSSHDAOTestDB(t))
	session := &model.SSHSession{
		SessionID:   "sess-1",
		HostID:      1,
		HostName:    "dev",
		HostAddress: "10.0.0.1",
		Status:      "connected",
	}
	require.NoError(t, d.Create(session))

	got, err := d.FindBySessionID("sess-1")
	require.NoError(t, err)
	assert.Equal(t, "connected", got.Status)
}

func TestSSHSessionDAO_UpdateStatus_ShouldPersist(t *testing.T) {
	d := NewSSHSessionDAO(setupSSHDAOTestDB(t))
	require.NoError(t, d.Create(&model.SSHSession{
		SessionID: "sess-2", HostID: 1, HostAddress: "10.0.0.1", Status: "connected",
	}))
	require.NoError(t, d.UpdateStatus("sess-2", "disconnected", "done"))

	got, err := d.FindBySessionID("sess-2")
	require.NoError(t, err)
	assert.Equal(t, "disconnected", got.Status)
	assert.Equal(t, "done", got.ErrorMsg)
}

func TestSSHSessionDAO_ListConnected_ReturnsOnlyConnected(t *testing.T) {
	d := NewSSHSessionDAO(setupSSHDAOTestDB(t))
	require.NoError(t, d.Create(&model.SSHSession{
		SessionID: "sess-3", HostID: 1, HostAddress: "10.0.0.1", Status: "connected",
	}))
	require.NoError(t, d.Create(&model.SSHSession{
		SessionID: "sess-4", HostID: 1, HostAddress: "10.0.0.1", Status: "disconnected",
	}))
	rows, err := d.ListConnected()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "sess-3", rows[0].SessionID)
}

// 主机密钥指纹：首次记录、地址变化后需要重新信任、删除主机时一并清掉。
func TestSSHHostDAO_HostKeyPinLifecycle(t *testing.T) {
	d := NewSSHHostDAO(setupSSHDAOTestDB(t))
	host := &model.SSHHost{Name: "a", Address: "1.1.1.1", Port: 22, User: "u"}
	require.NoError(t, d.Create(host))
	pin, err := d.HostKeyPin(host.ID, "1.1.1.1:22")
	require.NoError(t, err)
	assert.Empty(t, pin)
	require.NoError(t, d.SetHostKeyPin(host.ID, "1.1.1.1:22", "SHA256:abc"))
	pin, _ = d.HostKeyPin(host.ID, "1.1.1.1:22")
	assert.Equal(t, "SHA256:abc", pin)
	pin, _ = d.HostKeyPin(host.ID, "2.2.2.2:22")
	assert.Empty(t, pin, "地址变化后不沿用旧指纹")
	require.NoError(t, d.Delete(host.ID))
	pin, _ = d.HostKeyPin(host.ID, "1.1.1.1:22")
	assert.Empty(t, pin)
}
