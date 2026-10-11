package ssh

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"path"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/idgen"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/conn"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/pkg/sftp"

	"trpc.group/trpc-go/trpc-go/log"
)

// ServiceImpl SSH 服务实现
type ServiceImpl struct {
	hostDAO    *dao.SSHHostDAO
	sessionDAO *dao.SSHSessionDAO
	sessionMgr *conn.SessionManager
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

func (s *ServiceImpl) CreateSession(ctx context.Context, hostID int, clientIP, userID string) (string, error) {
	host, err := s.hostDAO.FindByID(hostID)
	if err != nil {
		return "", fmt.Errorf("主机不存在: %w", err)
	}

	sessionID := idgen.GenerateID(16)
	sshConn := &conn.SSHConn{
		Host:      host,
		HostKeys:  s.hostDAO,
		SessionID: sessionID,
		UserID:    userID,
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

func (s *ServiceImpl) GetSessionConn(sessionID string) (*conn.SSHConn, bool) {
	return s.sessionMgr.Load(sessionID)
}

func (s *ServiceImpl) SessionBelongsToUser(sessionID, userID string) bool {
	sshConn, ok := s.sessionMgr.Load(sessionID)
	return ok && sshConn != nil && userID != "" && sshConn.UserID == userID
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
	var failed []string
	for _, file := range files {
		if err := uploadOneFile(ctx, sftpCreator{sftpClient}, dstPath, file); err != nil {
			log.WarnContextf(ctx, "[SSH SFTP] 上传 %s 失败: %v", file.Filename, err)
			failed = append(failed, file.Filename)
			continue
		}
		uploaded = append(uploaded, file.Filename)
	}
	if len(failed) > 0 {
		// 部分或全部失败必须让调用方知道：返回已成功的文件名，同时返回错误，前端据此显示失败而不是“上传成功”。
		return uploaded, fmt.Errorf("以下文件上传失败: %s", strings.Join(failed, ", "))
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

// remoteCreator 是上传需要的远端文件创建能力（*sftp.Client 通过适配器提供），便于在没有 SFTP 服务的测试里注入失败。
type remoteCreator interface {
	Create(path string) (io.WriteCloser, error)
}

type sftpCreator struct{ client *sftp.Client }

func (c sftpCreator) Create(path string) (io.WriteCloser, error) { return c.client.Create(path) }

// uploadOneFile 上传单个文件；写入或关闭远端文件出错都算失败（关闭时才会 flush 的错误也要检查）。
func uploadOneFile(ctx context.Context, client remoteCreator, dstPath string, file *multipart.FileHeader) error {
	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("打开上传文件: %w", err)
	}
	defer src.Close()
	dst, err := client.Create(path.Join(dstPath, file.Filename))
	if err != nil {
		return fmt.Errorf("创建远程文件: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("写入远程文件: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("关闭远程文件: %w", err)
	}
	return nil
}
