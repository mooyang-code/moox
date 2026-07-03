package dao

import (
	"context"

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

// ListMonitorHosts 获取需要采集监控指标的 SSH 主机列表。
// hostIDs 为空时返回所有 SSH 主机。
func (d *SSHHostDAO) ListMonitorHosts(ctx context.Context, hostIDs []int) ([]*MonitorHost, error) {
	query := d.db.WithContext(ctx).
		Table("t_ssh_host")

	if len(hostIDs) > 0 {
		query = query.Where("c_id IN ?", hostIDs)
	}

	var hosts []*MonitorHost
	err := query.Select("c_id, c_name, c_address").Scan(&hosts).Error
	if err != nil {
		return nil, err
	}

	return hosts, nil
}
