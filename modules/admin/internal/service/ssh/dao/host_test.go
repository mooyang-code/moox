package dao

import (
	"context"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testKey = "0123456789abcdef0123456789abcdef"

func setupHostDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", testKey)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return db
}

func insertHost(t *testing.T, db *gorm.DB, id, address, reachability string, port int) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO t_hosts (c_host_id, c_address, c_reachability, c_ssh_port, c_ssh_user) VALUES (?, ?, ?, ?, 'ubuntu')`,
		id, address, reachability, port).Error)
}

func putCredential(t *testing.T, db *gorm.DB, hostID string, credential secretmodel.SSHCredential) {
	t.Helper()
	plain, err := credential.Encode()
	require.NoError(t, err)
	require.NoError(t, secretdao.PutSystemSecret(db, testKey, &secretmodel.Secret{
		SecretID: secretmodel.SSHHostSecretID(hostID), Name: "SSH " + hostID, Category: secretmodel.SSHCategory,
		SecretType: credential.Type(), KeyID: "ubuntu", SecretValue: plain, ExtraConfig: "{}", Status: "active",
	}))
}

func TestHostDAOTargetComposesRegistryAndCredential(t *testing.T) {
	db := setupHostDB(t)
	insertHost(t, db, "storage", "198.51.100.7", "direct", 2222)
	putCredential(t, db, "storage", secretmodel.SSHCredential{PrivateKey: "KEY", Passphrase: "phrase"})

	target, err := NewHostDAO(db).Target(context.Background(), "storage")
	require.NoError(t, err)
	assert.Equal(t, "storage", target.HostID)
	assert.Equal(t, "198.51.100.7:2222", target.Endpoint())
	assert.Equal(t, "ubuntu", target.User)
	assert.Equal(t, "KEY", target.Credential.PrivateKey)
	assert.Equal(t, "phrase", target.Credential.Passphrase)

	var stored secretmodel.Secret
	require.NoError(t, db.Where("c_secret_id = ?", secretmodel.SSHHostSecretID("storage")).First(&stored).Error)
	assert.NotContains(t, stored.SecretValue, "KEY", "凭据必须加密存储")
}

func TestHostDAOTargetRejectsUnregisteredAndUnavailableHosts(t *testing.T) {
	db := setupHostDB(t)
	d := NewHostDAO(db)
	_, err := d.Target(context.Background(), "missing")
	require.ErrorIs(t, err, ErrHostNotFound)

	insertHost(t, db, "noshell", "198.51.100.8", "direct", 0)
	_, err = d.Target(context.Background(), "noshell")
	require.ErrorIs(t, err, ErrSSHUnavailable, "端口为 0 表示控制台不能 SSH")

	insertHost(t, db, "factor", "192.168.0.103", "outbound", 22)
	putCredential(t, db, "factor", secretmodel.SSHCredential{Password: "p"})
	_, err = d.Target(context.Background(), "factor")
	require.ErrorIs(t, err, ErrSSHUnavailable, "只能出站的主机控制台连不上")

	insertHost(t, db, "nocred", "198.51.100.9", "direct", 22)
	_, err = d.Target(context.Background(), "nocred")
	require.ErrorIs(t, err, ErrSSHUnavailable, "没有凭据")
}

func TestPinHostKeyTrustsFirstKeyPerEndpoint(t *testing.T) {
	db := setupHostDB(t)
	insertHost(t, db, "storage", "198.51.100.7", "direct", 22)
	d := NewHostDAO(db)

	pinned, err := d.PinHostKey("storage", "198.51.100.7:22", "SHA256:first")
	require.NoError(t, err)
	assert.Equal(t, "SHA256:first", pinned)
	pinned, err = d.PinHostKey("storage", "198.51.100.7:22", "SHA256:other")
	require.NoError(t, err)
	assert.Equal(t, "SHA256:first", pinned, "同一地址端口已有指纹时不被覆盖")

	// 地址或端口变了：旧指纹自然失效，重新信任第一次看到的密钥。
	pinned, err = d.PinHostKey("storage", "198.51.100.7:2222", "SHA256:moved")
	require.NoError(t, err)
	assert.Equal(t, "SHA256:moved", pinned)
	pinned, err = d.PinHostKey("storage", "198.51.100.7:22", "SHA256:late")
	require.NoError(t, err)
	assert.Equal(t, "SHA256:late", pinned, "旧端点的连接不能把旧指纹写回")

	_, err = d.PinHostKey("missing", "198.51.100.7:22", "SHA256:x")
	require.ErrorIs(t, err, ErrHostNotFound)
}

func TestPinHostKeyConcurrentFirstConnectionsAgree(t *testing.T) {
	db := setupHostDB(t)
	insertHost(t, db, "storage", "198.51.100.7", "direct", 22)
	d := NewHostDAO(db)
	const workers = 8
	results := make(chan string, workers)
	var group sync.WaitGroup
	for i := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			pinned, err := d.PinHostKey("storage", "198.51.100.7:22", "SHA256:key"+string(rune('a'+i)))
			if err == nil {
				results <- pinned
			}
		}()
	}
	group.Wait()
	close(results)
	seen := map[string]bool{}
	for pinned := range results {
		seen[pinned] = true
	}
	assert.Len(t, seen, 1, "并发首次连接只信任一个指纹")
}
