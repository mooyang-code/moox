package setup

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testEncryptionKey = "setup-encryption-key-for-tests"

// setupDBWithoutAdmin 是还没有管理员的库：部署控制机之前的状态。
func setupDBWithoutAdmin(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(adminschema.AdminSQL()).Error)
	return db
}

// setupDB 是部署控制机之后的库：管理员已由 moox-admin-cli user ensure 按秘密文件建好。
func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupDBWithoutAdmin(t)
	manifest := testManifest()
	hash, err := mooxsecurity.HashPassword(manifest.Admin.Password)
	require.NoError(t, err)
	require.NoError(t, db.Where("c_username = ?", manifest.Admin.Username).FirstOrCreate(&auth.User{UserID: "admin-user", Username: manifest.Admin.Username, PasswordHash: hash}).Error)
	return db
}

func testManifest() Manifest {
	return Manifest{
		Admin:        Admin{Username: "admin", Password: "admin-password"},
		TencentCloud: TencentCloud{SecretID: "secret-id", SecretKey: "secret-key"},
	}
}

// 管理员不由 setup 建：还没有管理员时报错，不写任何记录。
func TestApplyRequiresDeployedAdmin(t *testing.T) {
	db := setupDBWithoutAdmin(t)
	_, err := NewService(db, testEncryptionKey).Apply(context.Background(), testManifest())
	require.ErrorIs(t, err, ErrAdminMissing)
	var users, secrets int64
	require.NoError(t, db.Model(&auth.User{}).Count(&users).Error)
	require.NoError(t, db.Model(&secretmodel.Secret{}).Count(&secrets).Error)
	assert.Zero(t, users)
	assert.Zero(t, secrets)
}

func TestApplyCreatesAllRecordsAndInspectCompletes(t *testing.T) {
	db := setupDB(t)
	service := NewService(db, testEncryptionKey)
	manifest := testManifest()

	result, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, 1, result.Users)
	assert.Equal(t, 1, result.Secrets)

	var user auth.User
	require.NoError(t, db.Where("c_username = ?", manifest.Admin.Username).First(&user).Error)
	assert.True(t, mooxsecurity.VerifyPassword(manifest.Admin.Password, user.PasswordHash))

	var secret secretmodel.Secret
	require.NoError(t, db.Where("c_secret_id = ?", tencentSecretID).First(&secret).Error)
	assert.Equal(t, "cloud", secret.Category)
	assert.Equal(t, "tencent", secret.Provider)
	assert.Equal(t, manifest.TencentCloud.SecretID, secret.KeyID)
	assert.NotEqual(t, manifest.TencentCloud.SecretKey, secret.SecretValue)
	assert.NotEqual(t, "{}", secret.ExtraConfig)
	require.NoError(t, secretdao.DecryptSecretFields(&secret, testEncryptionKey))
	assert.Equal(t, manifest.TencentCloud.SecretKey, secret.SecretValue)
	assert.Equal(t, "{}", secret.ExtraConfig)

	status, err := service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)
	assert.Equal(t, 0, status.Missing)
	assert.Empty(t, status.Kept)

	var setupTables int64
	require.NoError(t, db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='t_system_setup'").Scan(&setupTables).Error)
	assert.Zero(t, setupTables)
}

func TestApplyExactRetryIsUnchanged(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	manifest := testManifest()
	_, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)

	result, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "unchanged", result.Action)
}

func TestApplyConcurrentCallsConvergeToOneCreated(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	manifest := testManifest()
	const workers = 8
	actions := make(chan string, workers)
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := service.Apply(context.Background(), manifest)
			if err != nil {
				errorsSeen <- err
				return
			}
			actions <- result.Action
		}()
	}
	group.Wait()
	close(actions)
	close(errorsSeen)
	require.Empty(t, errorsSeen)
	created, unchanged := 0, 0
	for action := range actions {
		if action == "created" {
			created++
		} else if action == "unchanged" {
			unchanged++
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, workers-1, unchanged)
}

// 管理台改过管理员密码、或库里已有别的超级用户：以管理台为准，不改写，其余照常写入。
func TestApplyKeepsTheConsoleAdministrator(t *testing.T) {
	for name, user := range map[string]auth.User{
		"changed_password": {UserID: "u1", Username: testManifest().Admin.Username},
		"other_user":       {UserID: "u2", Username: "other-admin"},
	} {
		t.Run(name, func(t *testing.T) {
			db := setupDBWithoutAdmin(t)
			hash, err := mooxsecurity.HashPassword("changed-in-console")
			require.NoError(t, err)
			user.PasswordHash = hash
			require.NoError(t, db.Create(&user).Error)

			result, err := NewService(db, testEncryptionKey).Apply(context.Background(), testManifest())
			require.NoError(t, err)
			assert.Equal(t, []string{"admin"}, result.Kept)
			var stored auth.User
			require.NoError(t, db.Where("c_user_id = ?", user.UserID).First(&stored).Error)
			assert.True(t, mooxsecurity.VerifyPassword("changed-in-console", stored.PasswordHash), "管理台的密码不能被改写")
			var users, secrets int64
			require.NoError(t, db.Model(&auth.User{}).Count(&users).Error)
			require.NoError(t, db.Model(&secretmodel.Secret{}).Count(&secrets).Error)
			assert.Equal(t, int64(1), users)
			assert.Equal(t, int64(1), secrets, "腾讯云密钥照常写入")

			status, err := NewService(db, testEncryptionKey).Inspect(context.Background(), testManifest())
			require.NoError(t, err)
			assert.Equal(t, "completed", status.State)
			assert.Equal(t, []string{"admin"}, status.Kept)
		})
	}
}

// 管理台换过腾讯云密钥：保留管理台的，交易所密钥照常写入（本来的问题：一项不一致就整体回滚，交易所密钥写不进去）。
func TestApplyKeepsTheConsoleTencentSecretAndStillWritesTheRest(t *testing.T) {
	db := setupDB(t)
	record := &secretmodel.Secret{
		SecretID: tencentSecretID, Name: "Tencent Cloud Default", Category: "cloud", Provider: "tencent",
		SecretType: "api_key", KeyID: "different-id", SecretValue: "different-secret", ExtraConfig: "{}", Status: "active",
	}
	require.NoError(t, secretdao.EncryptSecretFields(record, testEncryptionKey))
	require.NoError(t, db.Create(record).Error)

	result, err := NewService(db, testEncryptionKey).Apply(context.Background(), testManifest())
	require.NoError(t, err)
	assert.Equal(t, "unchanged", result.Action)
	assert.Equal(t, []string{"tencent_cloud"}, result.Kept)
	var stored secretmodel.Secret
	require.NoError(t, db.Where("c_secret_id = ?", tencentSecretID).First(&stored).Error)
	assert.Equal(t, "different-id", stored.KeyID, "管理台的腾讯云密钥不能被改写")
	var users int64
	require.NoError(t, db.Model(&auth.User{}).Count(&users).Error)
	assert.Equal(t, int64(1), users)
}

func TestInspectReportsIncompleteWithoutWriting(t *testing.T) {
	db := setupDBWithoutAdmin(t)
	status, err := NewService(db, testEncryptionKey).Inspect(context.Background(), testManifest())
	require.NoError(t, err)
	assert.Equal(t, "incomplete", status.State)
	assert.Equal(t, 2, status.Missing)

	var users int64
	require.NoError(t, db.Model(&auth.User{}).Count(&users).Error)
	assert.Zero(t, users)
}

// 管理台主动硬删除默认腾讯云密钥后写入墓碑，setup init 不会静默复活；手工新建同 ID 后清除墓碑恢复可见。
func TestApplyDoesNotRecreateTencentSecretDeletedInConsole(t *testing.T) {
	db := setupDB(t)
	service := NewService(db, "  "+testEncryptionKey+"  ")
	manifest := testManifest()

	result, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, 1, result.Secrets)

	dao := secretdao.NewSecretDAO(db, testEncryptionKey)
	require.NoError(t, dao.Delete(context.Background(), tencentSecretID))

	result, err = service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "unchanged", result.Action)
	assert.Equal(t, 0, result.Secrets)
	assert.Empty(t, result.Kept)
	var count int64
	require.NoError(t, db.Model(&secretmodel.Secret{}).Where("c_secret_id = ?", tencentSecretID).Count(&count).Error)
	assert.Zero(t, count, "已硬删除的 tencent-default 不得被 setup init 静默复活")

	status, err := service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)
	assert.Zero(t, status.Missing)
	assert.Equal(t, 0, status.Secrets)

	// 手工新建同 ID 清除墓碑，后续 inspect 恢复以管理台为准。
	require.NoError(t, dao.Create(context.Background(), &secretmodel.Secret{
		SecretID: tencentSecretID, Name: "Tencent Cloud Default", Category: "cloud", Provider: "tencent",
		SecretType: "api_key", KeyID: "manual-id", SecretValue: "manual-secret", ExtraConfig: "{}", Status: "active",
	}))
	status, err = service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)
	assert.Equal(t, 1, status.Secrets)
	assert.Equal(t, []string{keptTencentCloud}, status.Kept)
}
