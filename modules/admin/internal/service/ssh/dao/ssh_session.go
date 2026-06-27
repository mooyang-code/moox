package dao

import (
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"gorm.io/gorm"
)

// SSHSessionDAO 会话数据访问层
type SSHSessionDAO struct {
	db *gorm.DB
}

// NewSSHSessionDAO 创建 DAO 实例
func NewSSHSessionDAO(db *gorm.DB) *SSHSessionDAO {
	return &SSHSessionDAO{db: db}
}

// Create 创建会话记录
func (d *SSHSessionDAO) Create(session *model.SSHSession) error {
	return d.db.Create(session).Error
}

// UpdateStatus 更新会话状态
func (d *SSHSessionDAO) UpdateStatus(sessionID, status, errMsg string) error {
	updates := map[string]interface{}{
		"c_status":     status,
		"c_close_time": time.Now(),
	}
	if errMsg != "" {
		updates["c_error_msg"] = errMsg
	}
	return d.db.Model(&model.SSHSession{}).
		Where("c_session_id = ?", sessionID).
		Updates(updates).Error
}

// FindBySessionID 根据会话 ID 查询
func (d *SSHSessionDAO) FindBySessionID(sessionID string) (*model.SSHSession, error) {
	var session model.SSHSession
	err := d.db.First(&session, "c_session_id = ?", sessionID).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// ListConnected 查询所有连接中的会话
func (d *SSHSessionDAO) ListConnected() ([]model.SSHSession, error) {
	var sessions []model.SSHSession
	err := d.db.Where("c_status = ?", "connected").
		Order("c_connect_time desc").Find(&sessions).Error
	return sessions, err
}

// CleanupStale 清理长时间未关闭的会话记录（标记为 disconnected）
func (d *SSHSessionDAO) CleanupStale(before time.Time) error {
	return d.db.Model(&model.SSHSession{}).
		Where("c_status = ? AND c_connect_time < ?", "connected", before).
		Updates(map[string]interface{}{
			"c_status":     "disconnected",
			"c_close_time": time.Now(),
			"c_error_msg":  "session cleanup: stale connection",
		}).Error
}
