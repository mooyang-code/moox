package api

// ========== 请求类型 ==========

// CreateHostRequest 创建主机请求
type CreateHostRequest struct {
	Name        string `json:"name" binding:"required,min=1,max=63"`
	Address     string `json:"address" binding:"required,min=1,max=128"`
	Port        int    `json:"port" binding:"required,gte=1,lte=65535"`
	User        string `json:"user" binding:"required,min=1,max=128"`
	Password    string `json:"password"`
	AuthType    string `json:"auth_type" binding:"required,oneof=pwd cert"`
	NetType     string `json:"net_type" binding:"required,oneof=tcp4 tcp6"`
	CertData    string `json:"cert_data"`
	CertPwd     string `json:"cert_pwd"`
	FontSize    int    `json:"font_size"`
	Background  string `json:"background"`
	Foreground  string `json:"foreground"`
	CursorColor string `json:"cursor_color"`
	FontFamily  string `json:"font_family"`
	CursorStyle string `json:"cursor_style"`
	Shell       string `json:"shell"`
	PtyType     string `json:"pty_type"`
	InitCmd     string `json:"init_cmd"`
}

// UpdateHostRequest 更新主机请求
type UpdateHostRequest struct {
	ID          int    `json:"id" binding:"required"`
	Name        string `json:"name" binding:"required,min=1,max=63"`
	Address     string `json:"address" binding:"required,min=1,max=128"`
	Port        int    `json:"port" binding:"required,gte=1,lte=65535"`
	User        string `json:"user" binding:"required,min=1,max=128"`
	Password    string `json:"password"`
	AuthType    string `json:"auth_type" binding:"required,oneof=pwd cert"`
	NetType     string `json:"net_type" binding:"required,oneof=tcp4 tcp6"`
	CertData    string `json:"cert_data"`
	CertPwd     string `json:"cert_pwd"`
	FontSize    int    `json:"font_size"`
	Background  string `json:"background"`
	Foreground  string `json:"foreground"`
	CursorColor string `json:"cursor_color"`
	FontFamily  string `json:"font_family"`
	CursorStyle string `json:"cursor_style"`
	Shell       string `json:"shell"`
	PtyType     string `json:"pty_type"`
	InitCmd     string `json:"init_cmd"`
}

// DeleteHostRequest 删除主机请求
type DeleteHostRequest struct {
	ID int `json:"id" binding:"required"`
}

// ListHostsRequest 列表请求
type ListHostsRequest struct {
	Keyword string `json:"keyword"`
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
}

// ========== SSH 会话请求 ==========

// CreateSessionRequest 创建会话请求
type CreateSessionRequest struct {
	HostID int `json:"host_id" binding:"required"`
}

// DisconnectSessionRequest 断开会话请求
type DisconnectSessionRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

// ResizeWindowRequest 调整窗口请求
type ResizeWindowRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	W         int    `json:"w" binding:"required,gte=40,lte=8192"`
	H         int    `json:"h" binding:"required,gte=2,lte=4096"`
}

// ExecCommandRequest 执行命令请求
type ExecCommandRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Cmd       string `json:"cmd" binding:"required,min=1"`
}

// ========== SFTP 请求 ==========

// SftpListRequest SFTP 列表请求
type SftpListRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Path      string `json:"path" binding:"required"`
}

// SftpMkdirRequest SFTP 创建目录请求
type SftpMkdirRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Path      string `json:"path" binding:"required"`
}

// SftpDeleteRequest SFTP 删除请求
type SftpDeleteRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Path      string `json:"path" binding:"required"`
}

// ========== 会话管理请求 ==========

// ForceDisconnectRequest 强制断开请求
type ForceDisconnectRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}
