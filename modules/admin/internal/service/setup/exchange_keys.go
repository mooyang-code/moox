package setup

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gorm.io/gorm"
)

var exchangeKeyNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// exchangeDisplayNames 是交易所在密钥名称里的展示名；键同时是允许的交易所集合。
var exchangeDisplayNames = map[string]string{"binance": "Binance", "okx": "OKX"}

// exchangeEnvironments 是允许的环境。
var exchangeEnvironments = map[string]bool{"production": true, "testnet": true}

func exchangeKeyID(key ExchangeKey) string {
	return secretmodel.ExchangeSecretID(key.Exchange, key.Environment, key.Name)
}

// normalizeExchangeKeys 规整并校验 [[exchange_keys]]：名称只允许 [a-z0-9-]（不替用户改大小写），交易所、环境小写化后检查，固定 ID 不能重复。
func normalizeExchangeKeys(inputs []ExchangeKey) ([]ExchangeKey, error) {
	keys := make([]ExchangeKey, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		input.Name = strings.TrimSpace(input.Name)
		input.Exchange = strings.ToLower(strings.TrimSpace(input.Exchange))
		input.Environment = strings.ToLower(strings.TrimSpace(input.Environment))
		input.APIKey = strings.TrimSpace(input.APIKey)
		if _, ok := exchangeDisplayNames[input.Exchange]; !ok || !exchangeEnvironments[input.Environment] ||
			!exchangeKeyNamePattern.MatchString(input.Name) || input.APIKey == "" || input.SecretKey == "" {
			return nil, ErrInvalid
		}
		id := exchangeKeyID(input)
		if _, ok := seen[id]; ok {
			return nil, ErrInvalid
		}
		seen[id] = struct{}{}
		keys = append(keys, input)
	}
	return keys, nil
}

// inspectExchangeKey 按固定 ID 查密钥，连已软删除的行一起看：唯一索引是 (c_secret_id, c_is_deleted)，
// 同一个 ID 不能被第二次软删除，所以软删除后重建会撞索引；用户在管理台删掉的密钥（作废或泄露）也不该被重新建出来。
func (s *Service) inspectExchangeKey(ctx context.Context, tx *gorm.DB, key ExchangeKey) (string, error) {
	var rows []secretmodel.Secret
	if err := tx.WithContext(ctx).Where("c_secret_id = ?", exchangeKeyID(key)).Find(&rows).Error; err != nil {
		return "", ErrStorage
	}
	for _, row := range rows {
		if !row.IsDeleted {
			return ExchangeKeyExists, nil
		}
	}
	if len(rows) > 0 {
		return ExchangeKeyDeleted, nil
	}
	return ExchangeKeyMissing, nil
}

// ensureExchangeKey 只建不改：缺失时新建，已存在（以管理台为准）或已在管理台删除时原样返回。返回值第二项是本次新建的条数。
func (s *Service) ensureExchangeKey(ctx context.Context, tx *gorm.DB, key ExchangeKey) (ExchangeKeyResult, int, error) {
	result := ExchangeKeyResult{SecretID: exchangeKeyID(key), ReadOnly: key.ReadOnly}
	state, err := s.inspectExchangeKey(ctx, tx, key)
	if err != nil {
		return result, 0, err
	}
	result.State = state
	if state != ExchangeKeyMissing {
		return result, 0, nil
	}
	encrypted, err := mooxsecurity.Encrypt(key.SecretKey, s.encryptionKey)
	if err != nil {
		return result, 0, ErrStorage
	}
	extra, err := exchangeExtraConfig(key)
	if err != nil {
		return result, 0, ErrStorage
	}
	now := time.Now()
	secret := &secretmodel.Secret{
		SecretID: result.SecretID, Name: exchangeKeyName(key), Description: "由 moox.toml 的 [[exchange_keys]] 写入；写入后以管理台为准，修改 moox.toml 不会覆盖",
		Category: "exchange", Provider: key.Exchange, SecretType: "api_key", KeyID: key.APIKey, SecretValue: encrypted,
		ExtraConfig: extra, Status: "active", Creator: "setup", CreateTime: now, ModifyTime: now,
	}
	if err := tx.WithContext(ctx).Create(secret).Error; err != nil {
		return result, 0, ErrStorage
	}
	if err := s.afterWrite("exchange_key:" + result.SecretID); err != nil {
		return result, 0, err
	}
	result.State = ExchangeKeyCreated
	return result, 1, nil
}

func exchangeKeyName(key ExchangeKey) string {
	return exchangeDisplayNames[key.Exchange] + " " + key.Environment + " " + key.Name
}

// exchangeExtraConfig 生成 extra_config：值只能是字符串（Trade 按 map[string]string 解析）。
// passphrase 给 OKX；readonly=true 让 Trade 拒绝用只读密钥建执行账户。
func exchangeExtraConfig(key ExchangeKey) (string, error) {
	extra := map[string]string{}
	if key.Passphrase != "" {
		extra["passphrase"] = key.Passphrase
	}
	if key.ReadOnly {
		extra["readonly"] = "true"
	}
	raw, err := json.Marshal(extra)
	if err != nil {
		return "", errors.New("encode extra_config")
	}
	return string(raw), nil
}
