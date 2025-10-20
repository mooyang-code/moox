package model

import (
	"strings"
	"time"

	"github.com/mooyang-code/moox/server/internal/common/crypto"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudAccount 云账户配置
type CloudAccount struct {
	// ID 自增主键
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// AccountID 账户唯一标识
	AccountID string `gorm:"column:c_account_id;uniqueIndex:idx_account_invalid;size:100;not null" json:"account_id"`
	// AccountName 账户名称
	AccountName string `gorm:"column:c_account_name;size:200;not null" json:"account_name"`
	// Provider 云厂商（tencent/aliyun/aws）
	Provider string `gorm:"column:c_provider;index:idx_provider;size:50;not null" json:"provider"`
	// SecretID 密钥ID（加密存储）
	SecretID string `gorm:"column:c_secret_id;type:text;not null" json:"secret_id"`
	// SecretKey 密钥（加密存储）
	SecretKey string `gorm:"column:c_secret_key;type:text;not null" json:"secret_key"`
	// AppID 应用ID
	AppID string `gorm:"column:c_app_id;size:200;not null;default:''" json:"app_id"`
	// COSRegion COS区域
	COSRegion string `gorm:"column:c_cos_region;size:100;not null;default:''" json:"cos_region"`
	// COSBucket COS桶名
	COSBucket string `gorm:"column:c_cos_bucket;size:200;not null;default:''" json:"cos_bucket"`
	// ExtraConfig 额外配置（JSON格式，如region等）
	ExtraConfig string `gorm:"column:c_extra_config;type:text;not null;default:'{}'" json:"extra_config"`
	// Invalid 删除标记
	Invalid int `gorm:"column:c_invalid;uniqueIndex:idx_account_invalid;index:idx_invalid;not null;default:0" json:"invalid"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (c *CloudAccount) TableName() string {
	return "t_cloud_accounts"
}

// MaskSecretKey 对SecretKey进行脱敏处理
func (c *CloudAccount) MaskSecretKey() {
	if c.SecretKey == "" {
		return
	}

	// 如果密钥长度小于等于8位，全部隐藏
	if len(c.SecretKey) <= 8 {
		c.SecretKey = "********"
		return
	}

	// 保留前3位和后3位，中间用星号
	c.SecretKey = c.SecretKey[:3] + "********" + c.SecretKey[len(c.SecretKey)-3:]
}

// MaskSecretID 对SecretID进行脱敏处理
func (c *CloudAccount) MaskSecretID() {
	if c.SecretID == "" {
		return
	}

	// 如果密钥长度小于等于8位，全部隐藏
	if len(c.SecretID) <= 8 {
		c.SecretID = "********"
		return
	}

	// 保留前3位和后3位，中间用星号
	c.SecretID = c.SecretID[:3] + "********" + c.SecretID[len(c.SecretID)-3:]
}

// MaskSecrets 对SecretID和SecretKey进行脱敏处理
func (c *CloudAccount) MaskSecrets() {
	c.MaskSecretID()
	c.MaskSecretKey()
}

// IsMasked 判断SecretKey是否已经被脱敏
func (c *CloudAccount) IsMasked() bool {
	return strings.Contains(c.SecretKey, "*")
}

// BeforeCreate GORM 钩子：创建前加密敏感信息
func (c *CloudAccount) BeforeCreate(tx *gorm.DB) error {
	return c.encryptSecrets()
}

// BeforeUpdate GORM 钩子：更新前加密敏感信息
func (c *CloudAccount) BeforeUpdate(tx *gorm.DB) error {
	// 只有在秘钥未被脱敏时才加密
	if !c.IsMasked() {
		return c.encryptSecrets()
	}
	return nil
}

// AfterFind GORM 钩子：查询后解密敏感信息
func (c *CloudAccount) AfterFind(tx *gorm.DB) error {
	return c.decryptSecrets()
}

// encryptSecrets 加密敏感信息
func (c *CloudAccount) encryptSecrets() error {
	ctx := trpc.BackgroundContext()
	encryptionKey := crypto.GetEncryptionKey()

	// 加密 SecretID（如果不为空）
	if c.SecretID != "" && !strings.Contains(c.SecretID, "*") {
		encrypted, err := crypto.AESEncrypt(c.SecretID, encryptionKey)
		if err != nil {
			log.ErrorContextf(ctx, "[CloudNode] Failed to encrypt SecretID: %v", err)
			return err
		}
		c.SecretID = encrypted
	}

	// 加密 SecretKey（如果不为空且未脱敏）
	if c.SecretKey != "" && !c.IsMasked() {
		encrypted, err := crypto.AESEncrypt(c.SecretKey, encryptionKey)
		if err != nil {
			log.ErrorContextf(ctx, "[CloudNode] Failed to encrypt SecretKey: %v", err)
			return err
		}
		c.SecretKey = encrypted
	}

	return nil
}

// decryptSecrets 解密敏感信息
func (c *CloudAccount) decryptSecrets() error {
	ctx := trpc.BackgroundContext()
	encryptionKey := crypto.GetEncryptionKey()

	// 解密 SecretID
	if c.SecretID != "" {
		decrypted, err := crypto.AESDecrypt(c.SecretID, encryptionKey)
		if err != nil {
			log.ErrorContextf(ctx, "[CloudNode] Failed to decrypt SecretID: %v", err)
			return err
		}
		c.SecretID = decrypted
	}

	// 解密 SecretKey
	if c.SecretKey != "" {
		decrypted, err := crypto.AESDecrypt(c.SecretKey, encryptionKey)
		if err != nil {
			log.ErrorContextf(ctx, "[CloudNode] Failed to decrypt SecretKey: %v", err)
			return err
		}
		c.SecretKey = decrypted
	}

	return nil
}

// CloudAccountTableName 表名常量
const CloudAccountTableName = "t_cloud_accounts"

// 云厂商类型常量
const (
	CloudProviderTencent = "tencent" // 腾讯云
	CloudProviderAliyun  = "aliyun"  // 阿里云
	CloudProviderAWS     = "aws"     // AWS
)
