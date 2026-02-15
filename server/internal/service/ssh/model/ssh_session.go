package model

import "time"

// SSHSession SSH 会话表
type SSHSession struct {
	ID          int       `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	SessionID   string    `gorm:"column:c_session_id;not null;size:64;uniqueIndex" json:"session_id"`
	HostID      int       `gorm:"column:c_host_id;not null;index" json:"host_id"`
	HostName    string    `gorm:"column:c_host_name;size:64" json:"host_name"`
	HostAddress string    `gorm:"column:c_host_address;not null;size:128" json:"host_address"`
	ClientIP    string    `gorm:"column:c_client_ip;size:64" json:"client_ip"`
	Username    string    `gorm:"column:c_username;size:128" json:"username"`
	Status      string    `gorm:"column:c_status;not null;size:32;default:'connected'" json:"status"` // connected | disconnected | error
	ConnectTime time.Time `gorm:"column:c_connect_time;type:datetime" json:"connect_time"`
	CloseTime   time.Time `gorm:"column:c_close_time;type:datetime" json:"close_time"`
	ErrorMsg    string    `gorm:"column:c_error_msg;type:text" json:"error_msg"`
}

func (s *SSHSession) TableName() string {
	return "t_ssh_session"
}
