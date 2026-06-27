package dao

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// SSHHostDAO 实现 SSH 主机表的数据访问逻辑。
type SSHHostDAO struct {
	db *gorm.DB
}

func NewSSHHostDAO(db *gorm.DB) *SSHHostDAO {
	return &SSHHostDAO{db: db}
}

// MonitorHost 监控主机信息
type MonitorHost struct {
	ID      int    `gorm:"column:c_id"`
	Name    string `gorm:"column:c_name"`
	Address string `gorm:"column:c_address"`
}

// SetMonitorEnabled 设置监控启用状态
func (d *SSHHostDAO) SetMonitorEnabled(ctx context.Context, hostID int, enabled bool) error {
	enabledValue := 0
	if enabled {
		enabledValue = 1
	}

	return d.db.WithContext(ctx).
		Table("t_ssh_host").
		Where("c_id = ?", hostID).
		Update("c_monitor_enabled", enabledValue).Error
}

// IsMonitorEnabled 检查是否启用监控
func (d *SSHHostDAO) IsMonitorEnabled(ctx context.Context, hostID int) (bool, error) {
	var enabled int
	err := d.db.WithContext(ctx).
		Table("t_ssh_host").
		Where("c_id = ?", hostID).
		Select("c_monitor_enabled").
		Scan(&enabled).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, fmt.Errorf("host not found")
		}
		return false, err
	}
	return enabled == 1, nil
}

// GetHost 获取主机信息
func (d *SSHHostDAO) GetHost(ctx context.Context, hostID int) (*MonitorHost, error) {
	var host MonitorHost
	err := d.db.WithContext(ctx).
		Table("t_ssh_host").
		Where("c_id = ?", hostID).
		Select("c_id, c_name, c_address").
		Scan(&host).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("host not found")
		}
		return nil, err
	}
	return &host, nil
}

// ListMonitorHosts 获取启用监控的主机列表
// hostIDs 为空时返回所有启用监控的主机
func (d *SSHHostDAO) ListMonitorHosts(ctx context.Context, hostIDs []int) ([]*MonitorHost, error) {
	query := d.db.WithContext(ctx).
		Table("t_ssh_host").
		Where("c_monitor_enabled = 1")

	if len(hostIDs) > 0 {
		// 获取指定主机（需要是启用监控的）
		query = query.Where("c_id IN ?", hostIDs)
	}

	var hosts []*MonitorHost
	err := query.Select("c_id, c_name, c_address").Scan(&hosts).Error
	if err != nil {
		return nil, err
	}

	return hosts, nil
}
