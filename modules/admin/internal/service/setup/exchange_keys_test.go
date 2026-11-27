package setup

import (
	"context"
	"encoding/json"
	"testing"

	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func exchangeKeyManifest() Manifest {
	manifest := testManifest()
	manifest.ExchangeKeys = []ExchangeKey{
		{Name: "readonly", Exchange: "binance", Environment: "production", APIKey: "ro-key", SecretKey: "ro-secret", ReadOnly: true},
		{Name: "main", Exchange: "OKX", Environment: "Testnet", APIKey: "okx-key", SecretKey: "okx-secret", Passphrase: "okx-pass"},
	}
	return manifest
}

func secretRows(t *testing.T, service *Service, id string) []secretmodel.Secret {
	t.Helper()
	var rows []secretmodel.Secret
	require.NoError(t, service.db.Where("c_secret_id = ?", id).Find(&rows).Error)
	return rows
}

// 固定 ID 是小写三段；Collector 读只读 Key 用的正是 exchange-binance-production-readonly。
func TestExchangeKeyIDIsLowercaseThreeSegments(t *testing.T) {
	assert.Equal(t, "exchange-binance-production-readonly", exchangeKeyID(ExchangeKey{Name: "readonly", Exchange: "binance", Environment: "production"}))
	assert.Equal(t, "exchange-okx-testnet-main", exchangeKeyID(ExchangeKey{Name: "main", Exchange: "OKX", Environment: "Testnet"}))
}

func TestApplyWritesExchangeKeysIntoSecretMgr(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)

	result, err := service.Apply(context.Background(), exchangeKeyManifest())
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, 3, result.Secrets, "腾讯云一把加两把交易所密钥")
	assert.Equal(t, []ExchangeKeyResult{
		{SecretID: "exchange-binance-production-readonly", State: ExchangeKeyCreated, ReadOnly: true},
		{SecretID: "exchange-okx-testnet-main", State: ExchangeKeyCreated},
	}, result.ExchangeKeys)

	rows := secretRows(t, service, "exchange-binance-production-readonly")
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, "exchange", row.Category)
	assert.Equal(t, "binance", row.Provider)
	assert.Equal(t, "api_key", row.SecretType)
	assert.Equal(t, "ro-key", row.KeyID)
	assert.Equal(t, "active", row.Status)
	assert.Equal(t, "setup", row.Creator)
	assert.NotContains(t, row.ExtraConfig, "readonly", "c_extra_config 落库必须是密文")
	plain, err := mooxsecurity.Decrypt(row.SecretValue, testEncryptionKey)
	require.NoError(t, err)
	assert.Equal(t, "ro-secret", plain)
	plainExtra, err := mooxsecurity.Decrypt(row.ExtraConfig, testEncryptionKey)
	require.NoError(t, err)
	var extra map[string]string
	require.NoError(t, json.Unmarshal([]byte(plainExtra), &extra))
	assert.Equal(t, map[string]string{"readonly": "true"}, extra)

	rows = secretRows(t, service, "exchange-okx-testnet-main")
	require.Len(t, rows, 1)
	assert.Equal(t, "okx", rows[0].Provider)
	assert.NotContains(t, rows[0].ExtraConfig, "okx-pass", "OKX passphrase 不能明文落库")
	plainExtra, err = mooxsecurity.Decrypt(rows[0].ExtraConfig, testEncryptionKey)
	require.NoError(t, err)
	extra = nil
	require.NoError(t, json.Unmarshal([]byte(plainExtra), &extra))
	assert.Equal(t, map[string]string{"passphrase": "okx-pass"}, extra)

	// 没有 passphrase 也不是只读：extra_config 加密后的明文是空对象。
	manifest := testManifest()
	manifest.ExchangeKeys = []ExchangeKey{{Name: "trade", Exchange: "binance", Environment: "production", APIKey: "k", SecretKey: "s"}}
	_, err = service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	rawExtra := secretRows(t, service, "exchange-binance-production-trade")[0].ExtraConfig
	assert.NotEqual(t, "{}", rawExtra)
	plainExtra, err = mooxsecurity.Decrypt(rawExtra, testEncryptionKey)
	require.NoError(t, err)
	assert.Equal(t, "{}", plainExtra)
}

// 只建不改：管理台改过的密钥不会被 moox.toml 改回去，也不算冲突。
func TestApplyNeverOverwritesExistingExchangeKey(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	manifest := exchangeKeyManifest()
	_, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)

	dao := secretdao.NewSecretDAO(service.db, testEncryptionKey)
	existing, err := dao.FindByID(context.Background(), "exchange-okx-testnet-main")
	require.NoError(t, err)
	existing.KeyID = "rotated-key"
	existing.ExtraConfig = `{"passphrase":"rotated"}`
	require.NoError(t, dao.Update(context.Background(), existing))

	manifest.ExchangeKeys[1].APIKey = "different-from-console"
	manifest.ExchangeKeys[1].SecretKey = "different-secret"
	result, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "unchanged", result.Action)
	for _, item := range result.ExchangeKeys {
		assert.Equal(t, ExchangeKeyExists, item.State)
	}
	got, err := dao.FindByID(context.Background(), "exchange-okx-testnet-main")
	require.NoError(t, err)
	assert.Equal(t, "rotated-key", got.KeyID)
	assert.Equal(t, `{"passphrase":"rotated"}`, got.ExtraConfig)
	assert.Equal(t, "okx-secret", got.SecretValue)

	status, err := service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)
	assert.Empty(t, status.Kept)
}

// 已在管理台硬删除的密钥（作废或泄露）由墓碑表记录，不静默复活；手工新建或重新导入同 ID 时自动清除墓碑。
func TestApplyDoesNotRecreateExchangeKeyDeletedInConsole(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	manifest := exchangeKeyManifest()
	_, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)

	dao := secretdao.NewSecretDAO(service.db, testEncryptionKey)
	require.NoError(t, dao.Delete(context.Background(), "exchange-binance-production-readonly"))

	result, err := service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "unchanged", result.Action)
	assert.Equal(t, ExchangeKeyDeleted, result.ExchangeKeys[0].State)
	assert.Equal(t, ExchangeKeyExists, result.ExchangeKeys[1].State)
	assert.Equal(t, 2, result.Secrets, "腾讯云一把加一把还在的交易所密钥，已在管理台删除的不再算")
	assert.Empty(t, secretRows(t, service, "exchange-binance-production-readonly"), "硬删除后 t_secrets 无残留行且不复活")

	// 已删除算完成：否则 setup init 在 apply 之后要求 completed，会永远报未完成。
	status, err := service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)
	assert.Zero(t, status.Missing)
	assert.Equal(t, 2, status.Secrets, "已在管理台删除的密钥不算在应当存在的数量里")
	assert.Equal(t, ExchangeKeyDeleted, status.ExchangeKeys[0].State)

	// 手工重建/导入同 ID 会清除墓碑，后续 inspect 视为 exists。
	require.NoError(t, dao.Create(context.Background(), &secretmodel.Secret{
		SecretID: "exchange-binance-production-readonly", Name: "reimported", Category: "exchange", Provider: "binance",
		SecretType: "api_key", KeyID: "new-ro-key", SecretValue: "new-ro-secret", ExtraConfig: `{"readonly":"true"}`, Status: "active",
	}))
	status, err = service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, ExchangeKeyExists, status.ExchangeKeys[0].State)
}

func TestInspectReportsMissingExchangeKeysAndCountsSecrets(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	manifest := exchangeKeyManifest()
	_, err := service.Apply(context.Background(), testManifest())
	require.NoError(t, err)

	status, err := service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "incomplete", status.State)
	assert.Equal(t, 2, status.Missing)
	assert.Equal(t, 3, status.Secrets)
	assert.Equal(t, ExchangeKeyMissing, status.ExchangeKeys[0].State)

	_, err = service.Apply(context.Background(), manifest)
	require.NoError(t, err)
	status, err = service.Inspect(context.Background(), manifest)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)
}

// moox.toml 里删掉的交易所密钥不会从 SecretMgr 自动删除，避免误删正在用的账户。
func TestApplyKeepsExchangeKeysRemovedFromManifest(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	_, err := service.Apply(context.Background(), exchangeKeyManifest())
	require.NoError(t, err)

	result, err := service.Apply(context.Background(), testManifest())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Secrets)
	rows := secretRows(t, service, "exchange-okx-testnet-main")
	require.Len(t, rows, 1)
}

func TestApplyRejectsInvalidExchangeKeys(t *testing.T) {
	valid := ExchangeKey{Name: "main", Exchange: "binance", Environment: "production", APIKey: "k", SecretKey: "s"}
	for name, mutate := range map[string]func(*ExchangeKey){
		"名称含大写":     func(key *ExchangeKey) { key.Name = "Main" },
		"名称含下划线":    func(key *ExchangeKey) { key.Name = "main_key" },
		"名称为空":      func(key *ExchangeKey) { key.Name = "" },
		"名称过长":      func(key *ExchangeKey) { key.Name = "a123456789a123456789a123456789a123456789a" },
		"交易所不认识":    func(key *ExchangeKey) { key.Exchange = "bybit" },
		"环境不认识":     func(key *ExchangeKey) { key.Environment = "paper" },
		"缺 api_key": func(key *ExchangeKey) { key.APIKey = " " },
		"缺 secret":  func(key *ExchangeKey) { key.SecretKey = "" },
	} {
		t.Run(name, func(t *testing.T) {
			key := valid
			mutate(&key)
			manifest := testManifest()
			manifest.ExchangeKeys = []ExchangeKey{key}
			service := NewService(setupDB(t), testEncryptionKey)
			_, err := service.Apply(context.Background(), manifest)
			require.ErrorIs(t, err, ErrInvalid)
			_, err = service.Inspect(context.Background(), manifest)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}

	duplicate := valid
	duplicate.Exchange = "Binance"
	manifest := testManifest()
	manifest.ExchangeKeys = []ExchangeKey{valid, duplicate}
	_, err := NewService(setupDB(t), testEncryptionKey).Apply(context.Background(), manifest)
	require.ErrorIs(t, err, ErrInvalid)

	other := valid
	other.Environment = "testnet"
	manifest.ExchangeKeys = []ExchangeKey{valid, other}
	_, err = NewService(setupDB(t), testEncryptionKey).Apply(context.Background(), manifest)
	require.NoError(t, err)
}

// 交易所密钥写入失败时整个事务回滚，不留半截记录。
func TestApplyRollsBackOnExchangeKeyFailure(t *testing.T) {
	service := NewService(setupDB(t), testEncryptionKey)
	service.writeHook = func(stage string) error {
		if stage == "exchange_key:exchange-okx-testnet-main" {
			return assert.AnError
		}
		return nil
	}
	_, err := service.Apply(context.Background(), exchangeKeyManifest())
	require.ErrorIs(t, err, ErrStorage)
	assert.Empty(t, secretRows(t, service, "exchange-binance-production-readonly"))
	assert.Empty(t, secretRows(t, service, "exchange-okx-testnet-main"))
}
