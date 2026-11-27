// Package keys 登记与读取调用方的签名公钥：每台主机一把（t_hosts.c_sign_public_key），
// 不属于主机的身份各一把（t_identity_keys）。Admin 不保存任何签名私钥；一个身份只有一把公钥，换密钥没有新旧并存。
package keys

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidIdentity  = errors.New("不是可以登记公钥的身份：只有 moox-cli 与服务目录里的外部身份")
	ErrInvalidPublicKey = errors.New("签名公钥必须是 base64 编码的 32 字节 ed25519 公钥")
	ErrNotRegistered    = errors.New("没有登记签名公钥")
)

// IdentityKey 是一个不属于主机的身份登记的公钥（base64）。
type IdentityKey struct {
	Identity  string    `gorm:"column:c_identity;primaryKey" json:"identity"`
	PublicKey string    `gorm:"column:c_sign_public_key" json:"public_key"`
	UpdatedAt time.Time `gorm:"column:c_mtime" json:"updated_at"`
}

func (IdentityKey) TableName() string { return "t_identity_keys" }

// HostKey 是一台主机登记的签名公钥（base64）；还没签发过身份包的主机为空。
type HostKey struct {
	HostID    string `json:"host_id"`
	PublicKey string `json:"public_key"`
}

type Store struct {
	db      *gorm.DB
	catalog servicecatalog.Catalog
}

// NewStore 不建表、不生成任何密钥。
func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("签名公钥登记需要数据库")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	return &Store{db: db, catalog: catalog}, nil
}

// ValidIdentity 报告名字是不是可以登记公钥的身份。组件名、console、host-gateway 都不行：
// 它们用主机密钥签名，登记成身份会让持有这把私钥的人不经主机就冒充它们。
func (s *Store) ValidIdentity(identity string) bool {
	return identity == servicecatalog.OperatorMooxCLI || s.catalog.ExternalIdentity(identity)
}

func (s *Store) check(identity, publicKey string) error {
	if !s.ValidIdentity(identity) {
		return ErrInvalidIdentity
	}
	if _, err := gatewayauth.ParsePublicKey(publicKey); err != nil {
		return ErrInvalidPublicKey
	}
	return nil
}

// SetIdentity 登记或替换一个身份的公钥。替换后旧私钥的签名在各网关拿到新快照后（几秒内）失效。
func (s *Store) SetIdentity(ctx context.Context, identity, publicKey string) error {
	if err := s.check(identity, publicKey); err != nil {
		return err
	}
	row := IdentityKey{Identity: identity, PublicKey: publicKey, UpdatedAt: time.Now().UTC()}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_identity"}}, UpdateAll: true}).Create(&row).Error
}

// EnsureIdentity 只在这个身份还没登记时写入，返回登记着的公钥（bootstrap 用：重复执行不改已有内容）。
func (s *Store) EnsureIdentity(ctx context.Context, identity, publicKey string) (string, error) {
	if err := s.check(identity, publicKey); err != nil {
		return "", err
	}
	row := IdentityKey{Identity: identity, PublicKey: publicKey, UpdatedAt: time.Now().UTC()}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return "", err
	}
	return s.Identity(ctx, identity)
}

// Identity 返回一个身份登记的公钥。
func (s *Store) Identity(ctx context.Context, identity string) (string, error) {
	var rows []IdentityKey
	if err := s.db.WithContext(ctx).Where("c_identity = ?", identity).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", ErrNotRegistered
	}
	return rows[0].PublicKey, nil
}

// DeleteIdentity 删除一个身份的公钥，它的私钥随即不能再用。
func (s *Store) DeleteIdentity(ctx context.Context, identity string) error {
	result := s.db.WithContext(ctx).Where("c_identity = ?", identity).Delete(&IdentityKey{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotRegistered
	}
	return nil
}

// Identities 按名字顺序返回登记着的全部身份公钥。库里不是合法身份或公钥格式不对的行（只可能是手工改库）不返回。
func (s *Store) Identities(ctx context.Context) ([]IdentityKey, error) {
	return s.IdentitiesTx(ctx, s.db)
}

// IdentitiesTx 在指定事务句柄上读取全部合法身份公钥，便于复用长生命周期 Store。
func (s *Store) IdentitiesTx(ctx context.Context, tx *gorm.DB) ([]IdentityKey, error) {
	var rows []IdentityKey
	if err := tx.WithContext(ctx).Order("c_identity").Find(&rows).Error; err != nil {
		return nil, err
	}
	return slices.DeleteFunc(rows, func(row IdentityKey) bool { return s.check(row.Identity, row.PublicKey) != nil }), nil
}

// HostPublicKey 返回一台主机登记的签名公钥；主机没登记时报错，登记了但还没签发过时为空。
func (s *Store) HostPublicKey(ctx context.Context, hostID string) (string, error) {
	var keys []string
	if err := s.db.WithContext(ctx).Table("t_hosts").Where("c_host_id = ?", hostID).Pluck("c_sign_public_key", &keys).Error; err != nil {
		return "", err
	}
	if len(keys) != 1 {
		return "", gorm.ErrRecordNotFound
	}
	return keys[0], nil
}

// SetHostPublicKey 写一台已登记主机的签名公钥（签发身份包时）。
func (s *Store) SetHostPublicKey(ctx context.Context, hostID, publicKey string) error {
	if _, err := gatewayauth.ParsePublicKey(publicKey); err != nil {
		return ErrInvalidPublicKey
	}
	result := s.db.WithContext(ctx).Table("t_hosts").Where("c_host_id = ?", hostID).
		Updates(map[string]any{"c_sign_public_key": publicKey, "c_mtime": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Hosts 按主机 ID 顺序返回全部登记主机的签名公钥。
func (s *Store) Hosts(ctx context.Context) ([]HostKey, error) {
	var rows []struct {
		HostID    string `gorm:"column:c_host_id"`
		PublicKey string `gorm:"column:c_sign_public_key"`
	}
	if err := s.db.WithContext(ctx).Table("t_hosts").Select("c_host_id, c_sign_public_key").Order("c_host_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	hosts := make([]HostKey, 0, len(rows))
	for _, row := range rows {
		hosts = append(hosts, HostKey{HostID: row.HostID, PublicKey: row.PublicKey})
	}
	return hosts, nil
}
