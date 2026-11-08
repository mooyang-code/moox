package setup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	adminspace "github.com/mooyang-code/moox/modules/admin/internal/service/space"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testEncryptionKey = "setup-encryption-key-for-tests"

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(adminschema.AdminSQL()).Error)
	return db
}

func testManifest() Manifest {
	return Manifest{
		Admin:        Admin{Username: "admin", Password: "admin-password"},
		TencentCloud: TencentCloud{SecretID: "secret-id", SecretKey: "secret-key"},
	}
}

func setupSpaceManifest() Manifest {
	manifest := testManifest()
	manifest.Spaces = []Space{{
		SpaceID: "stockcn", Name: "A股市场", Description: "A股行情与基本面数据",
		Owner: "quant", Market: "CN", Timezone: "Asia/Shanghai", Status: "active",
		AttributesJSON: `{"managed_by":"moox-cli","priority":1}`,
	}}
	return manifest
}

func TestApplyCreatesAndInspectsSpaces(t *testing.T) {
	db := setupDB(t)
	service := NewService(db, testEncryptionKey)

	result, err := service.Apply(context.Background(), setupSpaceManifest())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Spaces)
	assert.Equal(t, 1, result.SpacesCreated)
	assert.Zero(t, result.SpacesUnchanged)

	var stored adminspace.Space
	require.NoError(t, db.Where("c_space_id = ?", "stockcn").First(&stored).Error)
	assert.Equal(t, `{"managed_by":"moox-cli","priority":1}`, stored.Attributes)

	status, err := service.Inspect(context.Background(), setupSpaceManifest())
	require.NoError(t, err)
	assert.Equal(t, 1, status.Spaces)
	assert.Equal(t, "completed", status.State)
}

func TestApplyIdenticalSpaceCanonicalJSONIsUnchanged(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	manifest := setupSpaceManifest()
	_, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)

	manifest.Spaces[0].AttributesJSON = `{ "priority": 1, "managed_by": "moox-cli" }`
	result, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "unchanged", result.Action)
	assert.Zero(t, result.SpacesCreated)
	assert.Equal(t, 1, result.SpacesUnchanged)
}

// 业务空间与 moox.toml 不一致时以管理台为准：不改写、不报错，记进 Kept。
func TestApplyKeepsADifferentSpace(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	manifest := setupSpaceManifest()
	_, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)

	manifest.Spaces[0].Name = "不同名称"
	result, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, []string{"space:" + manifest.Spaces[0].SpaceID}, result.Kept)
	status, err := service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)
	assert.Equal(t, []string{"space:" + manifest.Spaces[0].SpaceID}, status.Kept)
}

func TestApplyRollsBackSpaceOnLaterFailure(t *testing.T) {
	db := setupDB(t)
	service := NewService(db, testEncryptionKey)
	service.writeHook = func(stage string) error {
		if stage == "space:stockcn" {
			return errors.New("injected space failure")
		}
		return nil
	}

	_, err := service.Apply(context.Background(), setupSpaceManifest())
	require.ErrorIs(t, err, ErrStorage)
	var spaces int64
	require.NoError(t, db.Model(&adminspace.Space{}).Count(&spaces).Error)
	assert.Zero(t, spaces)
}

func TestApplyRejectsInvalidAndDuplicateSpaces(t *testing.T) {
	for _, mutate := range []func(*Manifest){
		func(manifest *Manifest) { manifest.Spaces[0].AttributesJSON = "[]" },
		func(manifest *Manifest) { manifest.Spaces[0].AttributesJSON = `{"bad":` },
		func(manifest *Manifest) { manifest.Spaces[0].SpaceID = "" },
		func(manifest *Manifest) { manifest.Spaces = append(manifest.Spaces, manifest.Spaces[0]) },
	} {
		manifest := setupSpaceManifest()
		mutate(&manifest)
		_, err := NewService(setupDB(t), testEncryptionKey).Apply(context.Background(), manifest)
		require.ErrorIs(t, err, ErrInvalid)
	}
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

	var user authmodel.User
	require.NoError(t, db.Where("c_username = ?", manifest.Admin.Username).First(&user).Error)
	assert.True(t, mooxsecurity.VerifyPassword(manifest.Admin.Password, user.PasswordHash))

	var secret secretmodel.Secret
	require.NoError(t, db.Where("c_secret_id = ? AND c_is_deleted = 0", tencentSecretID).First(&secret).Error)
	assert.Equal(t, "cloud", secret.Category)
	assert.Equal(t, "tencent", secret.Provider)
	assert.Equal(t, manifest.TencentCloud.SecretID, secret.KeyID)
	assert.NotEqual(t, manifest.TencentCloud.SecretKey, secret.SecretValue)
	secretValue, err := mooxsecurity.Decrypt(secret.SecretValue, testEncryptionKey)
	require.NoError(t, err)
	assert.Equal(t, manifest.TencentCloud.SecretKey, secretValue)

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

func TestApplyCompletesMatchingPartialState(t *testing.T) {
	db := setupDB(t)
	manifest := testManifest()
	hash, err := mooxsecurity.HashPassword(manifest.Admin.Password)
	require.NoError(t, err)
	require.NoError(t, db.Create(&authmodel.User{
		UserID: "existing-user", Username: manifest.Admin.Username, PasswordHash: hash,
	}).Error)

	result, err := NewService(db, testEncryptionKey).Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)

	var users int64
	require.NoError(t, db.Model(&authmodel.User{}).Count(&users).Error)
	assert.Equal(t, int64(1), users)
	var secrets int64
	require.NoError(t, db.Model(&secretmodel.Secret{}).Count(&secrets).Error)
	assert.Equal(t, int64(1), secrets)
}

// 管理台改过管理员密码、或库里已有别的超级用户：以管理台为准，不新建、不改写，其余照常写入。
func TestApplyKeepsTheConsoleAdministrator(t *testing.T) {
	for name, user := range map[string]authmodel.User{
		"changed_password": {UserID: "u1", Username: testManifest().Admin.Username},
		"other_user":       {UserID: "u2", Username: "other-admin"},
	} {
		t.Run(name, func(t *testing.T) {
			db := setupDB(t)
			hash, err := mooxsecurity.HashPassword("changed-in-console")
			require.NoError(t, err)
			user.PasswordHash = hash
			require.NoError(t, db.Create(&user).Error)

			result, err := NewService(db, testEncryptionKey).Apply(context.Background(), testManifest())
			require.NoError(t, err)
			assert.Equal(t, []string{"admin"}, result.Kept)
			var stored authmodel.User
			require.NoError(t, db.Where("c_user_id = ?", user.UserID).First(&stored).Error)
			assert.True(t, mooxsecurity.VerifyPassword("changed-in-console", stored.PasswordHash), "管理台的密码不能被改写")
			var users, secrets int64
			require.NoError(t, db.Model(&authmodel.User{}).Count(&users).Error)
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

// 管理台换过腾讯云密钥：保留管理台的，管理员与交易所密钥照常写入（本来的问题：一项不一致就整体回滚，交易所密钥写不进去）。
func TestApplyKeepsTheConsoleTencentSecretAndStillWritesTheRest(t *testing.T) {
	db := setupDB(t)
	encrypted, err := mooxsecurity.Encrypt("different-secret", testEncryptionKey)
	require.NoError(t, err)
	require.NoError(t, db.Create(&secretmodel.Secret{
		SecretID: tencentSecretID, Name: "Tencent Cloud Default", Category: "cloud", Provider: "tencent",
		SecretType: "api_key", KeyID: "different-id", SecretValue: encrypted, ExtraConfig: "{}", Status: "active",
	}).Error)

	result, err := NewService(db, testEncryptionKey).Apply(context.Background(), testManifest())
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, []string{"tencent_cloud"}, result.Kept)
	var stored secretmodel.Secret
	require.NoError(t, db.Where("c_secret_id = ?", tencentSecretID).First(&stored).Error)
	assert.Equal(t, "different-id", stored.KeyID, "管理台的腾讯云密钥不能被改写")
	var users int64
	require.NoError(t, db.Model(&authmodel.User{}).Count(&users).Error)
	assert.Equal(t, int64(1), users)
}

func TestApplyInjectedFailureRollsBackAllWrites(t *testing.T) {
	for _, stage := range []string{"admin", "secret", "space:stockcn"} {
		t.Run(stage, func(t *testing.T) {
			db := setupDB(t)
			service := NewService(db, testEncryptionKey)
			service.writeHook = func(current string) error {
				if current == stage {
					return errors.New("injected write failure")
				}
				return nil
			}

			_, err := service.Apply(context.Background(), setupSpaceManifest())
			require.ErrorIs(t, err, ErrStorage)
			for name, model := range map[string]any{
				"users":   &authmodel.User{},
				"secrets": &secretmodel.Secret{},
				"spaces":  &adminspace.Space{},
			} {
				var count int64
				require.NoError(t, db.Model(model).Count(&count).Error)
				assert.Zero(t, count, name)
			}
		})
	}
}

func TestInspectReportsIncompleteWithoutWriting(t *testing.T) {
	db := setupDB(t)
	status, err := NewService(db, testEncryptionKey).Inspect(context.Background(), testManifest())
	require.NoError(t, err)
	assert.Equal(t, "incomplete", status.State)
	assert.Equal(t, 2, status.Missing)

	var users int64
	require.NoError(t, db.Model(&authmodel.User{}).Count(&users).Error)
	assert.Zero(t, users)
}
