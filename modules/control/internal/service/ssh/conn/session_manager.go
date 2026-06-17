package conn

import (
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// SessionManager SSH 会话管理器
type SessionManager struct {
	clients sync.Map // sessionID -> *SSHConn
}

// NewSessionManager 创建会话管理器
func NewSessionManager() *SessionManager {
	return &SessionManager{}
}

// Store 存储会话
func (m *SessionManager) Store(sessionID string, conn *SSHConn) {
	m.clients.Store(sessionID, conn)
}

// Load 加载会话
func (m *SessionManager) Load(sessionID string) (*SSHConn, bool) {
	cli, ok := m.clients.Load(sessionID)
	if !ok || cli == nil {
		return nil, false
	}
	conn, ok := cli.(*SSHConn)
	return conn, ok
}

// Delete 删除会话并关闭连接
func (m *SessionManager) Delete(sessionID string) {
	defer func() {
		if err := recover(); err != nil {
			log.Errorf("[SessionManager] Delete panic: %v", err)
		}
	}()

	cli, ok := m.clients.Load(sessionID)
	if !ok || cli == nil {
		return
	}

	m.clients.Delete(sessionID)

	conn, ok := cli.(*SSHConn)
	if !ok || conn == nil {
		return
	}
	conn.Close()
}

// Range 遍历所有会话
func (m *SessionManager) Range(fn func(sessionID string, conn *SSHConn) bool) {
	m.clients.Range(func(key, value any) bool {
		sessionID, ok := key.(string)
		if !ok {
			return true
		}
		conn, ok := value.(*SSHConn)
		if !ok {
			return true
		}
		return fn(sessionID, conn)
	})
}

// Count 在线会话数量
func (m *SessionManager) Count() int {
	count := 0
	m.clients.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// SessionInfo 会话简要信息（用于列表展示）
type SessionInfo struct {
	SessionID      string `json:"session_id"`
	HostID         int    `json:"host_id"`
	HostName       string `json:"host_name"`
	Address        string `json:"address"`
	Port           int    `json:"port"`
	User           string `json:"user"`
	ClientIP       string `json:"client_ip"`
	StartTime      string `json:"start_time"`
	LastActiveTime string `json:"last_active_time"`
}

// GetAllSessions 获取所有在线会话信息
func (m *SessionManager) GetAllSessions() []SessionInfo {
	var sessions []SessionInfo
	m.Range(func(sessionID string, conn *SSHConn) bool {
		sessions = append(sessions, SessionInfo{
			SessionID:      conn.SessionID,
			HostID:         conn.Host.ID,
			HostName:       conn.Host.Name,
			Address:        conn.Host.Address,
			Port:           conn.Host.Port,
			User:           conn.Host.User,
			ClientIP:       conn.ClientIP,
			StartTime:      conn.StartTime.Format("2006-01-02 15:04:05"),
			LastActiveTime: conn.LastActiveTime.Format("2006-01-02 15:04:05"),
		})
		return true
	})
	return sessions
}

// StartCleanupTimer 启动定时清理不活跃会话
func (m *SessionManager) StartCleanupTimer(interval, maxIdle time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			m.cleanInactiveSessions(maxIdle)
		}
	}()
}

// cleanInactiveSessions 清理不活跃会话
func (m *SessionManager) cleanInactiveSessions(maxIdle time.Duration) {
	defer func() {
		if err := recover(); err != nil {
			log.Errorf("[SessionManager] cleanInactiveSessions panic: %v", err)
		}
	}()

	now := time.Now()
	m.Range(func(sessionID string, conn *SSHConn) bool {
		if conn.LastActiveTime.Add(maxIdle).Before(now) {
			log.Infof("[SessionManager] 清理不活跃会话: %s (最后活跃: %s)", sessionID, conn.LastActiveTime.Format("2006-01-02 15:04:05"))
			m.Delete(sessionID)
		}
		return true
	})
}
