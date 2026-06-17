package ssh

import (
	"context"
	"mime/multipart"

	"github.com/mooyang-code/moox/modules/control/internal/service/ssh/conn"
	"github.com/mooyang-code/moox/modules/control/internal/service/ssh/model"
	"github.com/pkg/sftp"
)

// Service SSH 服务接口
type Service interface {
	// 主机配置
	CreateHost(ctx context.Context, host *model.SSHHost) error
	UpdateHost(ctx context.Context, host *model.SSHHost) error
	DeleteHost(ctx context.Context, id int) error
	GetHost(ctx context.Context, id int) (*model.SSHHost, error)
	ListHosts(ctx context.Context, keyword string, offset, limit int) ([]model.SSHHost, int64, error)

	// SSH 会话
	CreateSession(ctx context.Context, hostID int, clientIP string) (string, error)
	DisconnectSession(ctx context.Context, sessionID string) error
	ResizeWindow(ctx context.Context, sessionID string, w, h int) error
	ExecCommand(ctx context.Context, sessionID string, cmd string) (string, error)
	GetSessionConn(sessionID string) (*conn.SSHConn, bool)

	// SFTP
	SftpList(ctx context.Context, sessionID string, dirPath string) (interface{}, error)
	SftpUpload(ctx context.Context, sessionID string, dstPath string, files []*multipart.FileHeader) ([]string, error)
	SftpDownload(ctx context.Context, sessionID, filePath string) (*sftp.File, int64, string, error)
	SftpDelete(ctx context.Context, sessionID, path string) error
	SftpMkdir(ctx context.Context, sessionID, path string) error

	// 会话管理
	GetOnlineSessions(ctx context.Context) []conn.SessionInfo
	ForceDisconnect(ctx context.Context, sessionID string) error

	// 获取会话管理器（供 WebSocket handler 使用）
	GetSessionManager() *conn.SessionManager
}
