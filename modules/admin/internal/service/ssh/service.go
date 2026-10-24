package ssh

import (
	"context"
	"mime/multipart"

	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/conn"
	"github.com/pkg/sftp"
)

// Service SSH 服务接口
type Service interface {
	// SSH 会话。主机来自登记表 t_hosts，终端与文件只能连接登记了 SSH 信息的主机。
	CreateSession(ctx context.Context, hostID, clientIP, userID string) (string, error)
	DisconnectSession(ctx context.Context, sessionID string) error
	// CloseHostSessions 关闭某台主机上全部在线会话（主机连接信息变化或被删除后调用）。
	CloseHostSessions(hostID string)
	ResizeWindow(ctx context.Context, sessionID string, w, h int) error
	GetSessionConn(sessionID string) (*conn.SSHConn, bool)
	SessionBelongsToUser(sessionID, userID string) bool

	// SFTP
	SftpList(ctx context.Context, sessionID string, dirPath string) (interface{}, error)
	SftpUpload(ctx context.Context, sessionID string, dstPath string, files []*multipart.FileHeader) ([]string, error)
	SftpDownload(ctx context.Context, sessionID, filePath string) (*sftp.File, int64, string, error)
	SftpDelete(ctx context.Context, sessionID, path string) error
	SftpMkdir(ctx context.Context, sessionID, path string) error

	// 获取会话管理器（供 WebSocket handler 使用）
	GetSessionManager() *conn.SessionManager
}
