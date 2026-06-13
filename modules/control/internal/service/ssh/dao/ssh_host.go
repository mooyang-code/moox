package dao

import (
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/common/crypto"
	"github.com/mooyang-code/moox/modules/control/internal/service/ssh/model"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// SSHHostDAO 主机配置数据访问层
type SSHHostDAO struct {
	db *gorm.DB
}

// NewSSHHostDAO 创建 DAO 实例
func NewSSHHostDAO(db *gorm.DB) *SSHHostDAO {
	return &SSHHostDAO{db: db}
}

// Create 创建主机配置
func (d *SSHHostDAO) Create(host *model.SSHHost) error {
	if err := d.encryptSensitiveFields(host); err != nil {
		return err
	}
	host.CreateTime = time.Now()
	host.ModifyTime = time.Now()
	return d.db.Create(host).Error
}

// Update 更新主机配置
func (d *SSHHostDAO) Update(host *model.SSHHost) error {
	if err := d.encryptSensitiveFields(host); err != nil {
		return err
	}
	host.ModifyTime = time.Now()
	return d.db.Model(host).Where("c_id = ?", host.ID).
		Select("*").Omit("c_id", "c_ctime").Updates(host).Error
}

// Delete 删除主机配置
func (d *SSHHostDAO) Delete(id int) error {
	return d.db.Unscoped().Delete(&model.SSHHost{}, "c_id = ?", id).Error
}

// FindByID 根据 ID 查询
func (d *SSHHostDAO) FindByID(id int) (*model.SSHHost, error) {
	var host model.SSHHost
	err := d.db.First(&host, "c_id = ?", id).Error
	if err != nil {
		return nil, err
	}
	if err := d.decryptSensitiveFields(&host); err != nil {
		return nil, err
	}
	return &host, nil
}

// List 分页查询主机列表
func (d *SSHHostDAO) List(offset, limit int) ([]model.SSHHost, int64, error) {
	var hosts []model.SSHHost
	var total int64

	db := d.db.Model(&model.SSHHost{})
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := db.Order("c_mtime desc").Offset(offset).Limit(limit).Find(&hosts).Error; err != nil {
		return nil, 0, err
	}

	// 解密敏感字段
	for i := range hosts {
		if err := d.decryptSensitiveFields(&hosts[i]); err != nil {
			log.Warnf("[SSH DAO] 解密主机 %d 的敏感字段失败: %v", hosts[i].ID, err)
		}
	}
	return hosts, total, nil
}

// Search 按名称或地址搜索
func (d *SSHHostDAO) Search(keyword string, offset, limit int) ([]model.SSHHost, int64, error) {
	var hosts []model.SSHHost
	var total int64

	db := d.db.Model(&model.SSHHost{}).
		Where("c_name LIKE ? OR c_address LIKE ?", "%"+keyword+"%", "%"+keyword+"%")

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := db.Order("c_mtime desc").Offset(offset).Limit(limit).Find(&hosts).Error; err != nil {
		return nil, 0, err
	}

	for i := range hosts {
		if err := d.decryptSensitiveFields(&hosts[i]); err != nil {
			log.Warnf("[SSH DAO] 解密主机 %d 的敏感字段失败: %v", hosts[i].ID, err)
		}
	}
	return hosts, total, nil
}

// encryptSensitiveFields 加密敏感字段
func (d *SSHHostDAO) encryptSensitiveFields(host *model.SSHHost) error {
	key := crypto.GetEncryptionKey()
	var err error

	if host.Password != "" {
		host.Password, err = crypto.AESEncrypt(host.Password, key)
		if err != nil {
			return err
		}
	}
	if host.CertData != "" {
		host.CertData, err = crypto.AESEncrypt(host.CertData, key)
		if err != nil {
			return err
		}
	}
	if host.CertPwd != "" {
		host.CertPwd, err = crypto.AESEncrypt(host.CertPwd, key)
		if err != nil {
			return err
		}
	}
	return nil
}

// decryptSensitiveFields 解密敏感字段
func (d *SSHHostDAO) decryptSensitiveFields(host *model.SSHHost) error {
	key := crypto.GetEncryptionKey()
	var err error

	if host.Password != "" {
		host.Password, err = crypto.AESDecrypt(host.Password, key)
		if err != nil {
			return err
		}
	}
	if host.CertData != "" {
		host.CertData, err = crypto.AESDecrypt(host.CertData, key)
		if err != nil {
			return err
		}
	}
	if host.CertPwd != "" {
		host.CertPwd, err = crypto.AESDecrypt(host.CertPwd, key)
		if err != nil {
			return err
		}
	}
	return nil
}
