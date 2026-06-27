package conn

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/ssh"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"github.com/pkg/sftp"
	"golang.org/x/net/websocket"

	"trpc.group/trpc-go/trpc-go/log"
)

// SSHConn SSH 连接封装
type SSHConn struct {
	// 主机配置
	Host *model.SSHHost

	// 会话信息
	SessionID      string    `json:"session_id"`
	LastActiveTime time.Time `json:"last_active_time"`
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
		HostID         int    `json:"host_id"`
		HostName       string `json:"host_name"`
		Address        string `json:"address"`
		Port           int    `json:"port"`
		User           string `json:"user"`
		ClientIP       string `json:"client_ip"`
		LastActiveTime string `json:"last_active_time"`
		StartTime      string `json:"start_time"`
	}{
		SessionID:      s.SessionID,
		HostID:         s.Host.ID,
		HostName:       s.Host.Name,
		Address:        s.Host.Address,
		Port:           s.Host.Port,
		User:           s.Host.User,
		ClientIP:       s.ClientIP,
		LastActiveTime: s.LastActiveTime.Format("2006-01-02 15:04:05"),
		StartTime:      s.StartTime.Format("2006-01-02 15:04:05"),
	})
}

// Connect 建立 SSH 连接
func (s *SSHConn) Connect(clientIP string) error {
	s.ClientIP = clientIP
	s.StartTime = time.Now()
	s.LastActiveTime = time.Now()

	config := ssh.ClientConfig{
		User: s.Host.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(s.Host.Password),
			ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = s.Host.Password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	// 证书认证
	if s.Host.AuthType == "cert" {
		privateKeyBytes := []byte(s.Host.CertData)
		if s.Host.CertPwd != "" {
			signer, err := ssh.ParsePrivateKeyWithPassphrase(privateKeyBytes, []byte(s.Host.CertPwd))
			if err != nil {
				log.Errorf("[SSH Conn] 解析带密码私钥失败: %v", err)
				return fmt.Errorf("解析证书失败: %w", err)
			}
			config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
		} else {
			signer, err := ssh.ParsePrivateKey(privateKeyBytes)
			if err != nil {
				log.Errorf("[SSH Conn] 解析私钥失败: %v", err)
				return fmt.Errorf("解析证书失败: %w", err)
			}
			config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
		}
	}

	// 构建地址
	addr := fmt.Sprintf("%s:%d", s.Host.Address, s.Host.Port)
	if s.Host.NetType == "tcp6" {
		addr = fmt.Sprintf("[%s]:%d", s.Host.Address, s.Host.Port)
	}

	// 建立 SSH 连接
	sshClient, err := ssh.Dial(s.Host.NetType, addr, &config)
	if err != nil {
		return fmt.Errorf("SSH 连接失败: %w", err)
	}
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
	ptyType := s.Host.PtyType
	if ptyType == "" {
		ptyType = "xterm-256color"
	}
	if err := s.sshSession.RequestPty(ptyType, h, w, modes); err != nil {
		log.Errorf("[SSH Conn] RequestPty 失败: %v", err)
		return fmt.Errorf("RequestPty 失败: %w", err)
	}

	shell := s.Host.Shell
	if shell == "" {
		shell = "bash"
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

// ExecCommand 执行命令（创建新 session 执行）
func (s *SSHConn) ExecCommand(cmd string) (string, error) {
	session, err := s.sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建 SSH session 失败: %w", err)
	}
	defer session.Close()

	out, err := session.CombinedOutput(cmd)
	if err != nil {
		return string(out), fmt.Errorf("执行命令失败: %w", err)
	}
	return string(out), nil
}

// RefreshActiveTime 刷新活跃时间
func (s *SSHConn) RefreshActiveTime() {
	s.LastActiveTime = time.Now()
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
