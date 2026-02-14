package model

import "time"

// SSHHost SSH 主机配置表
type SSHHost struct {
	ID      int    `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	Name    string `gorm:"column:c_name;not null;size:64" json:"name"`
	Address string `gorm:"column:c_address;not null;size:128" json:"address"`
	Port    int    `gorm:"column:c_port;not null;default:22" json:"port"`
	User    string `gorm:"column:c_user;not null;size:128" json:"user"`
	// 认证信息（敏感字段，DAO 层加解密）
	Password string `gorm:"column:c_password;size:4096;default:''" json:"password,omitempty"`
	AuthType string `gorm:"column:c_auth_type;not null;size:32;default:'pwd'" json:"auth_type"` // pwd | cert
	NetType  string `gorm:"column:c_net_type;not null;size:32;default:'tcp4'" json:"net_type"`  // tcp4 | tcp6
	CertData string `gorm:"column:c_cert_data;type:text" json:"cert_data,omitempty"`
	CertPwd  string `gorm:"column:c_cert_pwd;size:128;default:''" json:"cert_pwd,omitempty"`
	// 终端外观配置
	FontSize    int    `gorm:"column:c_font_size;not null;default:14" json:"font_size"`
	Background  string `gorm:"column:c_background;not null;size:32;default:'#000000'" json:"background"`
	Foreground  string `gorm:"column:c_foreground;not null;size:32;default:'#FFFFFF'" json:"foreground"`
	CursorColor string `gorm:"column:c_cursor_color;not null;size:32;default:'#FFFFFF'" json:"cursor_color"`
	FontFamily  string `gorm:"column:c_font_family;not null;size:64;default:'Courier New'" json:"font_family"`
	CursorStyle string `gorm:"column:c_cursor_style;not null;size:32;default:'block'" json:"cursor_style"` // block | underline | bar
	// Shell 配置
	Shell   string `gorm:"column:c_shell;not null;size:64;default:'bash'" json:"shell"`
	PtyType string `gorm:"column:c_pty_type;not null;size:64;default:'xterm-256color'" json:"pty_type"`
	InitCmd string `gorm:"column:c_init_cmd;type:text" json:"init_cmd"`
	// 元数据
	Creator    string    `gorm:"column:c_creator;not null;default:''" json:"creator"`
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

func (s *SSHHost) TableName() string {
	return "t_ssh_host"
}
