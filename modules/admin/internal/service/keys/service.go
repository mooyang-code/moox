// Package keys 管理调用方签名密钥的主副本（设计文档 3.5）：生成、重复部署时复用、轮换。
//
// 主副本加密存放在 Admin 密钥表（t_secrets）：
//   - 内部调用方（组件、console、admin、moox-cli、access、host-gateway@<主机>）的服务签名密钥，
//     主机网关的校验密钥随网关控制的快照下发；
//   - 外部调用方（scf-collector、factor-engine、moox-skill）的密钥，只登记在外部接入上。
//
// KeyID 为 <身份>-<序号>。轮换时新旧 KeyID 同时有效，确认新密钥生效后再停用旧的。
package keys

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// 密钥的类别。
const (
	// CategoryCaller 是内部调用方的服务签名密钥。
	CategoryCaller = "gateway_caller"
	// CategoryPrincipal 是外部调用方的密钥，只登记在外部接入上。
	CategoryPrincipal = "access_principal"
)

const (
	systemSpaceID  = secretdao.SystemSpaceID
	statusActive   = "active"
	statusInactive = "inactive"
	secretBytes    = 32
	listPageSize   = 1000
)

// ErrNotFound 表示没有可用的密钥。
var ErrNotFound = errors.New("没有可用的密钥")

// Key 是一把密钥。
type Key struct {
	Category  string
	Caller    string
	KeyID     string
	Secret    string
	Active    bool
	Sequence  int
	CreatedAt time.Time
	secretID  string
}

// Credentials 转换为签名凭据。
func (k Key) Credentials() gatewayauth.Credentials {
	return gatewayauth.Credentials{KeyID: k.KeyID, Caller: k.Caller, Secret: k.Secret}
}

// File 转换为密钥文件内容。
func (k Key) File() gatewayauth.CallerKey {
	return gatewayauth.CallerKey{Caller: k.Caller, KeyID: k.KeyID, Secret: k.Secret}
}

// Service 读写密钥表。依赖 MOOX_ADMIN_ENCRYPTION_KEY 加解密。
type Service struct {
	secrets *secretdao.SecretDAO
}

// NewService 创建服务。
func NewService(secrets *secretdao.SecretDAO) *Service { return &Service{secrets: secrets} }

func validCategory(category string) error {
	if category != CategoryCaller && category != CategoryPrincipal {
		return fmt.Errorf("密钥类别 %q 无效", category)
	}
	return nil
}

// List 返回一个类别下某个身份的全部密钥（含已停用），按序号排序；caller 为空表示全部身份。
func (s *Service) List(ctx context.Context, category, caller string) ([]Key, error) {
	if err := validCategory(category); err != nil {
		return nil, err
	}
	var out []Key
	for offset := 0; ; offset += listPageSize {
		rows, total, err := s.secrets.List(ctx, offset, listPageSize, &secretdao.SecretFilters{Category: category, Provider: caller})
		if err != nil {
			return nil, fmt.Errorf("读取密钥表: %w", err)
		}
		for _, row := range rows {
			if row.SpaceID != systemSpaceID {
				continue
			}
			sequence, ok := keySequence(row.Provider, row.KeyID)
			if !ok {
				return nil, fmt.Errorf("密钥 %s 的 KeyID %q 不是 <身份>-<序号> 形式", row.SecretID, row.KeyID)
			}
			out = append(out, Key{
				Category: category, Caller: row.Provider, KeyID: row.KeyID, Secret: row.SecretValue,
				Active: row.Status == statusActive, Sequence: sequence, CreatedAt: row.CreateTime, secretID: row.SecretID,
			})
		}
		if int64(offset+len(rows)) >= total || len(rows) == 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Caller != out[j].Caller {
			return out[i].Caller < out[j].Caller
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out, nil
}

func keySequence(caller, keyID string) (int, bool) {
	suffix, ok := strings.CutPrefix(keyID, caller+"-")
	if !ok {
		return 0, false
	}
	sequence, err := strconv.Atoi(suffix)
	return sequence, err == nil && sequence > 0
}

// Ensure 返回身份当前的签名密钥；没有任何有效密钥时生成第一把。重复部署时一律复用。
func (s *Service) Ensure(ctx context.Context, category, caller string) (Key, bool, error) {
	key, err := s.SigningKey(ctx, category, caller)
	if err == nil {
		return key, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Key{}, false, err
	}
	created, err := s.create(ctx, category, caller)
	return created, err == nil, err
}

// SigningKey 返回身份最新的有效密钥，调用方用它签名。
func (s *Service) SigningKey(ctx context.Context, category, caller string) (Key, error) {
	keys, err := s.List(ctx, category, caller)
	if err != nil {
		return Key{}, err
	}
	for i := len(keys) - 1; i >= 0; i-- {
		if keys[i].Active {
			return keys[i], nil
		}
	}
	return Key{}, fmt.Errorf("%w: %s", ErrNotFound, caller)
}

// VerificationKeys 返回一组身份的全部有效密钥；轮换期间同一身份会有多把。
func (s *Service) VerificationKeys(ctx context.Context, category string, callers []string) ([]Key, error) {
	wanted := make(map[string]bool, len(callers))
	for _, caller := range callers {
		wanted[caller] = true
	}
	all, err := s.List(ctx, category, "")
	if err != nil {
		return nil, err
	}
	out := make([]Key, 0, len(callers))
	for _, key := range all {
		if key.Active && wanted[key.Caller] {
			out = append(out, key)
		}
	}
	return out, nil
}

// Rotate 为身份生成下一把密钥，新旧 KeyID 同时有效。
func (s *Service) Rotate(ctx context.Context, category, caller string) (Key, error) {
	if _, err := s.SigningKey(ctx, category, caller); err != nil {
		return Key{}, fmt.Errorf("身份 %s 还没有密钥，不能轮换: %w", caller, err)
	}
	return s.create(ctx, category, caller)
}

// Retire 停用一把旧密钥；不能停用身份最后一把有效密钥。
func (s *Service) Retire(ctx context.Context, category, caller, keyID string) error {
	keys, err := s.List(ctx, category, caller)
	if err != nil {
		return err
	}
	var target *Key
	active := 0
	for i := range keys {
		if keys[i].Active {
			active++
		}
		if keys[i].KeyID == keyID {
			target = &keys[i]
		}
	}
	switch {
	case target == nil:
		return fmt.Errorf("%w: %s", ErrNotFound, keyID)
	case !target.Active:
		return nil
	case active <= 1:
		return fmt.Errorf("密钥 %s 是 %s 最后一把有效密钥，不能停用", keyID, caller)
	}
	return s.secrets.UpdateStatus(ctx, target.secretID, statusInactive)
}

func (s *Service) create(ctx context.Context, category, caller string) (Key, error) {
	if err := validCategory(category); err != nil {
		return Key{}, err
	}
	caller = strings.TrimSpace(caller)
	if caller == "" {
		return Key{}, errors.New("身份不能为空")
	}
	keys, err := s.List(ctx, category, caller)
	if err != nil {
		return Key{}, err
	}
	sequence := 1
	if len(keys) > 0 {
		sequence = keys[len(keys)-1].Sequence + 1
	}
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return Key{}, fmt.Errorf("生成密钥: %w", err)
	}
	key := Key{
		Category: category, Caller: caller, KeyID: caller + "-" + strconv.Itoa(sequence),
		Secret: base64.RawURLEncoding.EncodeToString(raw), Active: true, Sequence: sequence,
	}
	row := &secretmodel.Secret{
		SpaceID: systemSpaceID, SecretID: uuid.New().String(), Name: "签名密钥 " + key.KeyID,
		Description: categoryDescription(category), Category: category, Provider: caller, SecretType: "token",
		KeyID: key.KeyID, SecretValue: key.Secret, ExtraConfig: "{}", Status: statusActive, Creator: "moox",
	}
	if err := s.secrets.Create(ctx, row); err != nil {
		return Key{}, fmt.Errorf("写入密钥 %s: %w", key.KeyID, err)
	}
	key.secretID, key.CreatedAt = row.SecretID, row.CreateTime
	return key, nil
}

func categoryDescription(category string) string {
	if category == CategoryPrincipal {
		return "外部调用方的签名密钥，只登记在外部接入上"
	}
	return "内部调用方的服务签名密钥，校验密钥随网关控制的快照下发"
}
