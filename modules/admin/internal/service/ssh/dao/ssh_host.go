package dao

import (
	"time"

	adminsecurity "github.com/mooyang-code/moox/modules/admin/internal/security"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
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

// Delete 删除主机配置，并清掉它记录的主机密钥。
func (d *SSHHostDAO) Delete(id int) error {
	if err := d.db.Exec("DELETE FROM t_ssh_host_key WHERE c_host_id = ?", id).Error; err != nil {
		return err
	}
	return d.db.Unscoped().Delete(&model.SSHHost{}, "c_id = ?", id).Error
}

// HostKeyPin 返回主机在该地址上已记录的主机密钥指纹；没有记录（或地址已变）时返回空串。
func (d *SSHHostDAO) HostKeyPin(hostID int, address string) (string, error) {
	var rows []struct{ Fingerprint string }
	if err := d.db.Raw("SELECT c_fingerprint AS fingerprint FROM t_ssh_host_key WHERE c_host_id = ? AND c_address = ?", hostID, address).Scan(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Fingerprint, nil
}

// SetHostKeyPin 记录主机在该地址上的主机密钥指纹（地址变化时覆盖，需要重新信任）。
func (d *SSHHostDAO) SetHostKeyPin(hostID int, address, fingerprint string) error {
	return d.db.Exec(`INSERT INTO t_ssh_host_key(c_host_id, c_address, c_fingerprint, c_ctime) VALUES(?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(c_host_id) DO UPDATE SET c_address = excluded.c_address, c_fingerprint = excluded.c_fingerprint, c_ctime = CURRENT_TIMESTAMP`,
		hostID, address, fingerprint).Error
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
	key, err := adminsecurity.GetEncryptionKey()
	if err != nil {
		return err
	}

	if host.Password != "" {
		host.Password, err = mooxsecurity.Encrypt(host.Password, key)
		if err != nil {
			return err
		}
	}
	if host.CertData != "" {
		host.CertData, err = mooxsecurity.Encrypt(host.CertData, key)
		if err != nil {
			return err
		}
	}
	if host.CertPwd != "" {
		host.CertPwd, err = mooxsecurity.Encrypt(host.CertPwd, key)
		if err != nil {
			return err
		}
	}
	return nil
}

// decryptSensitiveFields 解密敏感字段
func (d *SSHHostDAO) decryptSensitiveFields(host *model.SSHHost) error {
	key, err := adminsecurity.GetEncryptionKey()
	if err != nil {
		return err
	}

	if host.Password != "" {
		host.Password, err = mooxsecurity.Decrypt(host.Password, key)
		if err != nil {
			return err
		}
	}
	if host.CertData != "" {
		host.CertData, err = mooxsecurity.Decrypt(host.CertData, key)
		if err != nil {
			return err
		}
	}
	if host.CertPwd != "" {
		host.CertPwd, err = mooxsecurity.Decrypt(host.CertPwd, key)
		if err != nil {
			return err
		}
	}
	return nil
}
