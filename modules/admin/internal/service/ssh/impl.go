package ssh

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"path"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/common"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/conn"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/pkg/sftp"

	"trpc.group/trpc-go/trpc-go/log"
)

// ServiceImpl SSH 服务实现
type ServiceImpl struct {
	hostDAO       *dao.SSHHostDAO
	sessionDAO *dao.SSHSessionDAO
	sessionMgr    *conn.SessionManager
}

// NewService 创建 SSH 服务
func NewService(hostDAO *dao.SSHHostDAO, sessionDAO *dao.SSHSessionDAO) *ServiceImpl {
	mgr := conn.NewSessionManager()
	// 每 30 秒检查一次，超过 30 分钟不活跃的会话自动清理
	mgr.StartCleanupTimer(30*time.Second, 30*time.Minute)

	return &ServiceImpl{
		hostDAO:    hostDAO,
		sessionDAO: sessionDAO,
		sessionMgr: mgr,
	}
}

// GetSessionManager 获取会话管理器
func (s *ServiceImpl) GetSessionManager() *conn.SessionManager {
	return s.sessionMgr
}

// ========== 主机配置 ==========

func (s *ServiceImpl) CreateHost(ctx context.Context, host *model.SSHHost) error {
	return s.hostDAO.Create(host)
}

func (s *ServiceImpl) UpdateHost(ctx context.Context, host *model.SSHHost) error {
	return s.hostDAO.Update(host)
}

func (s *ServiceImpl) DeleteHost(ctx context.Context, id int) error {
	return s.hostDAO.Delete(id)
}

func (s *ServiceImpl) GetHost(ctx context.Context, id int) (*model.SSHHost, error) {
	return s.hostDAO.FindByID(id)
}

func (s *ServiceImpl) ListHosts(ctx context.Context, keyword string, offset, limit int) ([]model.SSHHost, int64, error) {
	if keyword != "" {
		return s.hostDAO.Search(keyword, offset, limit)
	}
	return s.hostDAO.List(offset, limit)
}

// ========== SSH 会话 ==========

func (s *ServiceImpl) CreateSession(ctx context.Context, hostID int, clientIP string) (string, error) {
	host, err := s.hostDAO.FindByID(hostID)
	if err != nil {
		return "", fmt.Errorf("主机不存在: %w", err)
	}

	sessionID := common.GenerateID(16)
	sshConn := &conn.SSHConn{
		Host:      host,
		SessionID: sessionID,
	}

	if err := sshConn.Connect(clientIP); err != nil {
		return "", err
	}

	s.sessionMgr.Store(sessionID, sshConn)

	// 记录会话日志
	sessionLog := &model.SSHSession{
		SessionID:   sessionID,
		HostID:      host.ID,
		HostName:    host.Name,
		HostAddress: host.Address,
		ClientIP:    clientIP,
		Username:    host.User,
		Status:      "connected",
		ConnectTime: time.Now(),
	}
	if err := s.sessionDAO.Create(sessionLog); err != nil {
		log.WarnContextf(ctx, "[SSH Service] 记录会话日志失败: %v", err)
	}

	log.InfoContextf(ctx, "[SSH Service] 创建会话成功: sessionID=%s, host=%s:%d", sessionID, host.Address, host.Port)
	return sessionID, nil
}

func (s *ServiceImpl) DisconnectSession(ctx context.Context, sessionID string) error {
	s.sessionMgr.Delete(sessionID)
	// 更新会话日志
	if err := s.sessionDAO.UpdateStatus(sessionID, "disconnected", ""); err != nil {
		log.WarnContextf(ctx, "[SSH Service] 更新会话日志失败: %v", err)
	}
	log.InfoContextf(ctx, "[SSH Service] 断开会话: %s", sessionID)
	return nil
}

func (s *ServiceImpl) ResizeWindow(ctx context.Context, sessionID string, w, h int) error {
	conn, ok := s.sessionMgr.Load(sessionID)
	if !ok {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}
	conn.RefreshActiveTime()
	return conn.ResizeWindow(w, h)
}

func (s *ServiceImpl) ExecCommand(ctx context.Context, sessionID string, cmd string) (string, error) {
	conn, ok := s.sessionMgr.Load(sessionID)
	if !ok {
		return "", fmt.Errorf("会话不存在: %s", sessionID)
	}
	conn.RefreshActiveTime()
	return conn.ExecCommand(cmd)
}

func (s *ServiceImpl) GetSessionConn(sessionID string) (*conn.SSHConn, bool) {
	return s.sessionMgr.Load(sessionID)
}

// ========== SFTP ==========

func (s *ServiceImpl) SftpList(ctx context.Context, sessionID string, dirPath string) (interface{}, error) {
	sshConn, ok := s.sessionMgr.Load(sessionID)
	if !ok {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}
	sshConn.RefreshActiveTime()

	sftpClient := sshConn.GetSFTPClient()
	if sftpClient == nil {
		return nil, fmt.Errorf("SFTP 客户端不可用")
	}

	files, err := sftpClient.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}

	fileCount := 0
	dirCount := 0
	var fileList []map[string]interface{}
	for _, file := range files {
		fileInfo := map[string]interface{}{
			"path":     path.Join(dirPath, file.Name()),
			"name":     file.Name(),
			"mode":     file.Mode().String(),
			"size":     file.Size(),
			"mod_time": file.ModTime().Format("2006-01-02 15:04:05"),
		}
		if file.IsDir() {
			fileInfo["type"] = "d"
			dirCount++
		} else {
			fileInfo["type"] = "f"
			fileCount++
		}
		fileList = append(fileList, fileInfo)
	}

	// 解析路径面包屑
	paths := parsePaths(dirPath)

	return map[string]interface{}{
		"files":       fileList,
		"file_count":  fileCount,
		"dir_count":   dirCount,
		"paths":       paths,
		"current_dir": dirPath,
	}, nil
}

func (s *ServiceImpl) SftpUpload(ctx context.Context, sessionID string, dstPath string, files []*multipart.FileHeader) ([]string, error) {
	sshConn, ok := s.sessionMgr.Load(sessionID)
	if !ok {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}
	sshConn.RefreshActiveTime()

	sftpClient := sshConn.GetSFTPClient()
	if sftpClient == nil {
		return nil, fmt.Errorf("SFTP 客户端不可用")
	}

	var uploaded []string
	for _, file := range files {
		srcFile, err := file.Open()
		if err != nil {
			log.WarnContextf(ctx, "[SSH SFTP] 打开上传文件失败: %v", err)
			continue
		}
		dstFile, err := sftpClient.Create(path.Join(dstPath, file.Filename))
		if err != nil {
			srcFile.Close()
			log.WarnContextf(ctx, "[SSH SFTP] 创建远程文件失败: %v", err)
			continue
		}
		_, err = io.Copy(dstFile, srcFile)
		srcFile.Close()
		dstFile.Close()
		if err != nil {
			log.WarnContextf(ctx, "[SSH SFTP] 上传文件失败: %v", err)
			continue
		}
		uploaded = append(uploaded, file.Filename)
	}

	return uploaded, nil
}

func (s *ServiceImpl) SftpDownload(ctx context.Context, sessionID, filePath string) (*sftp.File, int64, string, error) {
	sshConn, ok := s.sessionMgr.Load(sessionID)
	if !ok {
		return nil, 0, "", fmt.Errorf("会话不存在: %s", sessionID)
	}
	sshConn.RefreshActiveTime()

	sftpClient := sshConn.GetSFTPClient()
	if sftpClient == nil {
		return nil, 0, "", fmt.Errorf("SFTP 客户端不可用")
	}

	file, err := sftpClient.Open(filePath)
	if err != nil {
		return nil, 0, "", fmt.Errorf("打开文件失败: %w", err)
	}

	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, "", fmt.Errorf("获取文件信息失败: %w", err)
	}

	return file, stat.Size(), stat.Name(), nil
}

func (s *ServiceImpl) SftpDelete(ctx context.Context, sessionID, filePath string) error {
	sshConn, ok := s.sessionMgr.Load(sessionID)
	if !ok {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}
	sshConn.RefreshActiveTime()

	sftpClient := sshConn.GetSFTPClient()
	if sftpClient == nil {
		return fmt.Errorf("SFTP 客户端不可用")
	}

	return sftpClient.RemoveAll(filePath)
}

func (s *ServiceImpl) SftpMkdir(ctx context.Context, sessionID, dirPath string) error {
	sshConn, ok := s.sessionMgr.Load(sessionID)
	if !ok {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}
	sshConn.RefreshActiveTime()

	sftpClient := sshConn.GetSFTPClient()
	if sftpClient == nil {
		return fmt.Errorf("SFTP 客户端不可用")
	}

	return sftpClient.MkdirAll(dirPath)
}

// ========== 会话管理 ==========

func (s *ServiceImpl) GetOnlineSessions(ctx context.Context) []conn.SessionInfo {
	return s.sessionMgr.GetAllSessions()
}

func (s *ServiceImpl) ForceDisconnect(ctx context.Context, sessionID string) error {
	return s.DisconnectSession(ctx, sessionID)
}

// ========== 内部工具 ==========

func parsePaths(dirPath string) []map[string]string {
	parts := strings.Split(dirPath, "/")
	var dirs []string
	if strings.HasPrefix(dirPath, "/") {
		dirs = append(dirs, "/")
	}
	for _, item := range parts {
		name := strings.TrimSpace(item)
		if len(name) > 0 {
			dirs = append(dirs, name)
		}
	}

	var paths []map[string]string
	for i, item := range dirs {
		fullPath := path.Join(dirs[:i+1]...)
		paths = append(paths, map[string]string{
			"name": item,
			"dir":  fullPath,
		})
	}
	return paths
}
