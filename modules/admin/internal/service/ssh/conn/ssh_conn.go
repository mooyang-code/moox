package conn

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/websocket"

	"trpc.group/trpc-go/trpc-go/log"
)

// 终端固定使用 xterm-256color 与 bash；外观（字体、配色）由浏览器本地保存。
const (
	ptyType = "xterm-256color"
	shell   = "bash"
)

// SSHConn SSH 连接封装
type SSHConn struct {
	// 连接目标
	Target model.Target
	// HostKeys 记录并校验服务端主机密钥。
	HostKeys HostKeyStore

	// 会话信息
	SessionID      string    `json:"session_id"`
	UserID         string    `json:"user_id"`
	LastActiveTime time.Time `json:"last_active_time"`
	activeMu       sync.RWMutex
	StartTime      time.Time `json:"start_time"`
	ClientIP       string    `json:"client_ip"`

	// 内部连接对象
	sshClient  *ssh.Client
	sftpClient *sftp.Client
	sshSession *ssh.Session
	ws         *websocket.Conn
}

// MarshalJSON 序列化时隐藏敏感信息
func (s *SSHConn) MarshalJSON() ([]byte, error) {
	return json.Marshal(&struct {
		SessionID      string `json:"session_id"`
		HostID         string `json:"host_id"`
		Address        string `json:"address"`
		Port           int    `json:"port"`
		User           string `json:"user"`
		ClientIP       string `json:"client_ip"`
		LastActiveTime string `json:"last_active_time"`
		StartTime      string `json:"start_time"`
	}{
		SessionID:      s.SessionID,
		HostID:         s.Target.HostID,
		Address:        s.Target.Address,
		Port:           s.Target.Port,
		User:           s.Target.User,
		ClientIP:       s.ClientIP,
		LastActiveTime: s.LastActive().Format("2006-01-02 15:04:05"),
		StartTime:      s.StartTime.Format("2006-01-02 15:04:05"),
	})
}

// HostKeyStore 记录每台主机已信任的 SSH 主机密钥指纹（SHA256）。
type HostKeyStore interface {
	// PinHostKey 原子地确定主机在该“地址:端口”上受信任的指纹：没有记录（或记录属于别的地址、端口）时记录 fingerprint（首次信任），
	// 已有记录则不改动；返回生效的指纹。并发的首次连接只有第一个会被记录，其余看到的是已记录的值。
	PinHostKey(hostID, endpoint, fingerprint string) (string, error)
}

// hostKeyCallback 校验服务端主机密钥：已记录指纹必须一致，不一致在发送任何认证信息之前拒绝；
// 首次连接记录指纹（TOFU），之后固定。没有配置存储时一律拒绝，不再接受任意主机密钥。
func (s *SSHConn) hostKeyCallback() ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if s.HostKeys == nil || s.Target.HostID == "" {
			return errors.New("未配置 SSH 主机密钥校验，拒绝连接")
		}
		address := s.Target.Endpoint()
		fingerprint := ssh.FingerprintSHA256(key)
		pinned, err := s.HostKeys.PinHostKey(s.Target.HostID, address, fingerprint)
		if err != nil {
			return fmt.Errorf("记录或读取主机密钥失败: %w", err)
		}
		if pinned != fingerprint {
			return fmt.Errorf("主机 %s 的密钥与已记录的指纹不一致，拒绝连接（可能被劫持，或主机已重装）", address)
		}
		return nil
	}
}

// Connect 建立 SSH 连接
func (s *SSHConn) Connect(clientIP string) error {
	s.ClientIP = clientIP
	s.StartTime = time.Now()
	s.RefreshActiveTime()

	credential := s.Target.Credential
	config := ssh.ClientConfig{
		User:            s.Target.User,
		HostKeyCallback: s.hostKeyCallback(),
		Timeout:         30 * time.Second,
	}
	if credential.PrivateKey != "" {
		privateKey := []byte(credential.PrivateKey)
		var signer ssh.Signer
		var err error
		if credential.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(privateKey, []byte(credential.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(privateKey)
		}
		if err != nil {
			log.Errorf("[SSH Conn] 解析私钥失败: %v", err)
			return fmt.Errorf("解析私钥失败: %w", err)
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		config.Auth = []ssh.AuthMethod{
			ssh.Password(credential.Password),
			ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = credential.Password
				}
				return answers, nil
			}),
		}
	}

	// 建立 SSH 连接
	sshClient, err := ssh.Dial("tcp", s.Target.Endpoint(), &config)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
	// 只有完整建连成功才把连接交给调用方（由会话管理器负责关闭）；中途失败在这里关闭，避免泄漏连接与文件描述符。
	connected := false
	defer func() {
		if connected {
			return
		}
		if s.sftpClient != nil {
			_ = s.sftpClient.Close()
			s.sftpClient = nil
		}
		_ = sshClient.Close()
		s.sshClient = nil
	}()
	s.sshClient = sshClient

	// 建立 SFTP 客户端
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		log.Warnf("[SSH Conn] 创建 SFTP 客户端失败: %v", err)
		// SFTP 失败不影响 SSH 终端功能
	}
	s.sftpClient = sftpClient

	// 创建 SSH Session
	sshSession, err := s.sshClient.NewSession()
	if err != nil {
		log.Errorf("[SSH Conn] 创建 SSH Session 失败: %v", err)
		return fmt.Errorf("创建 SSH Session 失败: %w", err)
	}
	s.sshSession = sshSession
	connected = true

	return nil
}

// activeReader 包装 io.Reader，每次读取数据时刷新会话活跃时间
type activeReader struct {
	reader io.Reader
	conn   *SSHConn
}

func (r *activeReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.conn.RefreshActiveTime()
	}
	return n, err
}

// RunTerminal 启动终端
func (s *SSHConn) RunTerminal(stdout, stderr io.Writer, stdin io.Reader, w, h int, ws *websocket.Conn) error {
	defer func() {
		if err := recover(); err != nil {
			log.Errorf("[SSH Conn] RunTerminal panic: %v", err)
		}
	}()

	s.ws = ws

	s.sshSession.Stdout = stdout
	s.sshSession.Stderr = stderr
	s.sshSession.Stdin = &activeReader{reader: stdin, conn: s}

	modes := ssh.TerminalModes{}
	if err := s.sshSession.RequestPty(ptyType, h, w, modes); err != nil {
		log.Errorf("[SSH Conn] RequestPty 失败: %v", err)
		return fmt.Errorf("RequestPty 失败: %w", err)
	}

	err := s.sshSession.Run(shell)
	if err != nil {
		log.Errorf("[SSH Conn] Run shell 失败: %v", err)
		return fmt.Errorf("Run shell 失败: %w", err)
	}
	return nil
}

// ResizeWindow 调整终端窗口大小
func (s *SSHConn) ResizeWindow(w, h int) error {
	if s.sshSession == nil {
		return fmt.Errorf("SSH session 不存在")
	}
	return s.sshSession.WindowChange(h, w)
}

// LastActive 返回最近活跃时间。终端输入、SFTP 请求在各自 goroutine 里刷新，清理线程同时读取，必须加锁。
func (s *SSHConn) LastActive() time.Time {
	s.activeMu.RLock()
	defer s.activeMu.RUnlock()
	return s.LastActiveTime
}

// RefreshActiveTime 刷新活跃时间
func (s *SSHConn) RefreshActiveTime() {
	s.activeMu.Lock()
	s.LastActiveTime = time.Now()
	s.activeMu.Unlock()
}

// GetSFTPClient 获取 SFTP 客户端
func (s *SSHConn) GetSFTPClient() *sftp.Client {
	return s.sftpClient
}

// Close 关闭所有连接
func (s *SSHConn) Close() {
	if s.ws != nil {
		s.ws.Close()
	}
	if s.sshSession != nil {
		s.sshSession.Close()
	}
	if s.sftpClient != nil {
		s.sftpClient.Close()
	}
	if s.sshClient != nil {
		s.sshClient.Close()
	}
}
