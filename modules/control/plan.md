
     当前 MooX
     的容器管理页面（/login#/container-management/container-list）中的 SSH
     终端功能仅为 Mock 模拟实现，无法真正连接远程主机。需要基于已 copy
     到项目中的 WebSSH
     开源代码（server/internal/service/ssh/），将其改造为与 MooX
     架构风格一致的完整 Web SSH 管理工具。

     目标功能

     - 多主机同时连接、重连、清屏
     - IPv4/IPv6 支持，SSH 密码/证书登录
     - 主机连接信息 CRUD（保存、编辑、直连）
     - SFTP 文件管理（上传/下载/创建目录/删除）
     - 终端自定义（字体大小、颜色、样式）
     - 后台管理：在线会话列表，强制断开

     ---
     一、数据库表定义

     使用 GORM + SQLite，遵循 MooX 现有命名规范（t_ 前缀表名，c_
     前缀列名）。

     表 1：t_ssh_host（主机配置表）

     // model/ssh_host.go
     type SSHHost struct {
         ID          int       `gorm:"primaryKey;column:c_id;autoIncrement" 
     json:"id"`
         Name        string    `gorm:"column:c_name;not null;size:64" 
     json:"name"`
         Address     string    `gorm:"column:c_address;not null;size:128" 
     json:"address"`
         Port        int       `gorm:"column:c_port;not null;default:22" 
     json:"port"`
         User        string    `gorm:"column:c_user;not null;size:128" 
     json:"user"`
         Password    string
     `gorm:"column:c_password;size:4096;default:''" json:"password"`
         AuthType    string    `gorm:"column:c_auth_type;not 
     null;size:32;default:'pwd'" json:"auth_type"`    // pwd | cert
         NetType     string    `gorm:"column:c_net_type;not 
     null;size:32;default:'tcp4'" json:"net_type"`      // tcp4 | tcp6
         CertData    string    `gorm:"column:c_cert_data;type:text" 
     json:"cert_data"`
         CertPwd     string    `gorm:"column:c_cert_pwd;size:128;default:''"
      json:"cert_pwd"`
         // 终端外观配置
         FontSize    int       `gorm:"column:c_font_size;not 
     null;default:14" json:"font_size"`
         Background  string    `gorm:"column:c_background;not 
     null;size:32;default:'#000000'" json:"background"`
         Foreground  string    `gorm:"column:c_foreground;not 
     null;size:32;default:'#FFFFFF'" json:"foreground"`
         CursorColor string    `gorm:"column:c_cursor_color;not 
     null;size:32;default:'#FFFFFF'" json:"cursor_color"`
         FontFamily  string    `gorm:"column:c_font_family;not 
     null;size:64;default:'Courier New'" json:"font_family"`
         CursorStyle string    `gorm:"column:c_cursor_style;not 
     null;size:32;default:'block'" json:"cursor_style"` // block | underline
      | bar
         // Shell 配置
         Shell       string    `gorm:"column:c_shell;not 
     null;size:64;default:'bash'" json:"shell"`
         PtyType     string    `gorm:"column:c_pty_type;not 
     null;size:64;default:'xterm-256color'" json:"pty_type"`
         InitCmd     string    `gorm:"column:c_init_cmd;type:text" 
     json:"init_cmd"`
         // 元数据
         Creator     string    `gorm:"column:c_creator;not null;default:''" 
     json:"creator"`
         CreateTime  time.Time
     `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" 
     json:"create_time"`
         ModifyTime  time.Time
     `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" 
     json:"modify_time"`
     }

     func (s *SSHHost) TableName() string { return "t_ssh_host" }

     敏感字段（password, cert_data, cert_pwd）在 DAO 层统一做 AES 
     加解密，复用 server/internal/common/crypto/ 下的工具函数。

     表 2：t_ssh_session（会话审计日志表）

     // model/ssh_session.go
     type SSHSession struct {
         ID          int       `gorm:"primaryKey;column:c_id;autoIncrement" 
     json:"id"`
         SessionID   string    `gorm:"column:c_session_id;not 
     null;size:64;index" json:"session_id"`
         HostID      int       `gorm:"column:c_host_id;not null;index" 
     json:"host_id"`
         HostAddress string    `gorm:"column:c_host_address;not 
     null;size:128" json:"host_address"`
         ClientIP    string    `gorm:"column:c_client_ip;size:64" 
     json:"client_ip"`
         Username    string    `gorm:"column:c_username;size:128" 
     json:"username"`
         Status      string    `gorm:"column:c_status;not 
     null;size:32;default:'connected'" json:"status"` // connected | 
     disconnected | error
         ConnectTime time.Time `gorm:"column:c_connect_time;type:datetime" 
     json:"connect_time"`
         CloseTime   time.Time `gorm:"column:c_close_time;type:datetime" 
     json:"close_time"`
         ErrorMsg    string    `gorm:"column:c_error_msg;type:text" 
     json:"error_msg"`
     }

     func (s *SSHSession) TableName() string { return "t_ssh_session"
      }

     ---
     二、后端架构（Go）

     2.1 目录结构重构

     将 server/internal/service/ssh/ 从当前的 "copy 照搬" 改造为 MooX 标准
     service 结构：

     server/internal/service/ssh/
     ├── model/
     │   ├── ssh_host.go           # SSHHost 模型
     │   └── ssh_session.go    # 会话日志模型
     ├── dao/
     │   ├── ssh_host_dao.go       # 主机配置 DAO（含 AES 加解密）
     │   └── ssh_session_dao.go# 会话日志 DAO
     ├── api/
     │   ├── router.go             # 路由注册（RegisterSSHRoutes）
     │   ├── host_handler.go       # 主机配置 CRUD handler
     │   ├── session_handler.go    # SSH 会话管理
     handler（create/connect/disconnect/resize）
     │   ├── sftp_handler.go       # SFTP 文件管理 handler
     │   ├── manage_handler.go     # 后台管理 handler（在线列表、强制断开）
     │   └── types.go              # 请求/响应结构体
     ├── conn/
     │   ├── ssh_conn.go           # SSH
     连接核心（connect、RunTerminal、ResizeWindow）
     │   └── session_manager.go    # 会话管理器（OnlineClients
     sync.Map、清理定时器）
     ├── gateway/
     │   ├── handler.go            # SSHGatewayHandler（实现 ServiceHandler
     接口）
     │   └── register.go           # 网关注册
     ├── service.go                # Service 接口定义
     └── impl.go                   # Service 实现（依赖注入 DAO +
     SessionManager）

     需要删除/不再使用的旧文件：ssh/app/、ssh/gin/、ssh/gorm/、ssh/mysql/、s
     sh/pgsql/、ssh/websocket/（保留 ssh/crypto/、ssh/sftp/、ssh/term/）

     2.2 Service 接口

     // service.go
     type Service interface {
         // 主机配置
         CreateHost(ctx context.Context, host *model.SSHHost) error
         UpdateHost(ctx context.Context, host *model.SSHHost) error
         DeleteHost(ctx context.Context, id int) error
         GetHost(ctx context.Context, id int) (*model.SSHHost, error)
         ListHosts(ctx context.Context, offset, limit int) ([]model.SSHHost,
      int64, error)

         // SSH 会话
         CreateSession(ctx context.Context, hostID int, clientIP string)
     (sessionID string, err error)
         DisconnectSession(ctx context.Context, sessionID string) error
         ResizeWindow(ctx context.Context, sessionID string, w, h int) error
         ExecCommand(ctx context.Context, sessionID string, cmd string)
     (string, error)

         // SFTP
         SftpList(ctx context.Context, sessionID string, dirPath string)
     (interface{}, error)
         SftpUpload(ctx context.Context, sessionID string, dstPath string,
     files []*multipart.FileHeader) error
         SftpDownload(ctx context.Context, sessionID, filePath string)
     (*sftp.File, os.FileInfo, error)
         SftpDelete(ctx context.Context, sessionID, path string) error
         SftpMkdir(ctx context.Context, sessionID, path string) error

         // 会话管理
         GetOnlineSessions(ctx context.Context) []SessionInfo
         ForceDisconnect(ctx context.Context, sessionID string) error
     }

     2.3 Gateway 集成

     遵循 CloudNode 的 Gateway 模式，创建 SSHGatewayHandler：

     // gateway/handler.go - 与 cloudnode/gateway/handler.go 同构
     type SSHGatewayHandler struct {
         engine    *gin.Engine
         serviceID string
     }

     func NewSSHGatewayHandler(sshService ssh.Service) *SSHGatewayHandler {
         engine := gin.New()
         engine.Use(gin.Recovery())
         api := engine.Group("/api/v1")
         sshapi.RegisterSSHRoutes(api, sshService)
         return &SSHGatewayHandler{engine: engine, serviceID: "ssh"}
     }

     注意：WebSocket 连接 不走 Gateway（Gateway 基于 httptest.NewRecorder
     无法代理 WebSocket）。WebSocket 端点继续在独立的 SSH HTTP
     服务上直接暴露，前端通过独立端口连接 WebSocket。

     2.4 API 路由设计

     // 通过 Control API 代理的 REST API（端口 20103）
     POST   /api/control/ssh/ListHosts           → 主机列表（分页）
     POST   /api/control/ssh/CreateHost          → 创建主机
     POST   /api/control/ssh/UpdateHost          → 更新主机
     POST   /api/control/ssh/DeleteHost          → 删除主机
     POST   /api/control/ssh/CreateSession       → 创建 SSH 会话（返回
     session_id）
     POST   /api/control/ssh/DisconnectSession   → 断开 SSH 会话
     POST   /api/control/ssh/ResizeWindow        → 调整终端大小
     POST   /api/control/ssh/ExecCommand         → 执行命令
     POST   /api/control/ssh/SftpList            → SFTP 目录列表
     POST   /api/control/ssh/SftpMkdir           → SFTP 创建目录
     POST   /api/control/ssh/SftpDelete          → SFTP 删除
     POST   /api/control/ssh/GetOnlineSessions   → 在线会话列表
     POST   /api/control/ssh/ForceDisconnect     → 强制断开

     // SSH 独立 HTTP 服务上的直连端点（端口 config 中配置，如 20180）
     GET    /api/ssh/conn?session_id=X&w=X&h=X    → WebSocket 终端连接
     GET    /api/sftp/download?session_id=X&path=X →
     文件下载（直接流式传输）
     PUT    /api/sftp/upload                       → 文件上传（multipart
     form）

     2.5 响应格式

     所有 REST API 统一使用 common.SuccessResponse / common.HandleAppError /
      common.PaginatedListResponse，输出格式：

     {
         "code": 200,
         "message": "ok",
         "data": [...],
         "total": 10
     }

     2.6 Bootstrap 集成

     在 bootstrap/services.go 中：
     1. 创建 SSH DAO（注入 dbManager.GetDB()）
     2. 创建 SSH Service 实现
     3. 创建 SSHGatewayHandler 并注册到 Gateway
     4. 启动 SSH 独立 HTTP 服务（仅提供 WebSocket 和文件传输端点）

     ---
     三、前端架构（Vue 3 + TypeScript）

     3.1 新增/修改文件列表

     web/src/
     ├── api/modules/
     │   └── ssh.ts                          # SSH 管理相关所有 API（新增）
     ├── views/container/
     │   ├── ssh-terminal/
     │   │   └── ssh-terminal.vue            # 重写：真实 xterm.js
     终端（替换当前 Mock）
     │   ├── ssh-hosts/
     │   │   └── ssh-hosts.vue               # 新增：主机管理页面
     │   ├── ssh-file-manager/
     │   │   └── ssh-file-manager.vue        # 新增：SFTP 文件管理页面
     │   └── ssh-sessions/
     │       └── ssh-sessions.vue            # 新增：在线会话管理页面
     ├── router/route.ts                     # 新增路由
     └── package.json                        # 新增依赖：@xterm/xterm,
     @xterm/addon-attach, @xterm/addon-fit

     3.2 路由规划

     在 route.ts 的 layout.children 中新增：

     {
         path: "/container-management/ssh-hosts",
         name: "ssh-hosts",
         component: () =>
     import("@/views/container/ssh-hosts/ssh-hosts.vue"),
         meta: { title: "主机管理" }
     },
     {
         path: "/container-management/ssh-terminal",
         name: "ssh-terminal",
         component: () =>
     import("@/views/container/ssh-terminal/ssh-terminal.vue"),
         meta: { title: "SSH终端" }
     },
     {
         path: "/container-management/ssh-file-manager",
         name: "ssh-file-manager",
         component: () =>
     import("@/views/container/ssh-file-manager/ssh-file-manager.vue"),
         meta: { title: "文件管理" }
     },
     {
         path: "/container-management/ssh-sessions",
         name: "ssh-sessions",
         component: () =>
     import("@/views/container/ssh-sessions/ssh-sessions.vue"),
         meta: { title: "会话管理" }
     }

     3.3 API 模块设计（api/modules/ssh.ts）

     import { api } from '@/api/config';  // 走 Gateway

     // ========== 主机配置 ==========
     export const listSSHHosts = (params: { offset?: number; limit?: number 
     }) =>
         api.post('/ssh/ListHosts', params);

     export const createSSHHost = (data: SSHHostForm) =>
         api.post('/ssh/CreateHost', data);

     export const updateSSHHost = (data: SSHHostForm) =>
         api.post('/ssh/UpdateHost', data);

     export const deleteSSHHost = (id: number) =>
         api.post('/ssh/DeleteHost', { id });

     // ========== SSH 会话 ==========
     export const createSSHSession = (data: { host_id: number }) =>
         api.post('/ssh/CreateSession', data);

     export const disconnectSSHSession = (sessionId: string) =>
         api.post('/ssh/DisconnectSession', { session_id: sessionId });

     export const resizeSSHTerminal = (sessionId: string, w: number, h: 
     number) =>
         api.post('/ssh/ResizeWindow', { session_id: sessionId, w, h });

     // ========== SFTP ==========
     export const sftpList = (sessionId: string, path: string) =>
         api.post('/ssh/SftpList', { session_id: sessionId, path });

     export const sftpMkdir = (sessionId: string, path: string) =>
         api.post('/ssh/SftpMkdir', { session_id: sessionId, path });

     export const sftpDelete = (sessionId: string, path: string) =>
         api.post('/ssh/SftpDelete', { session_id: sessionId, path });

     // 文件下载/上传走 SSH 独立端口（非 Gateway）
     export const getSftpDownloadUrl = (sessionId: string, path: string) =>
         `http://${window.location.hostname}:20180/api/sftp/download?session
     _id=${sessionId}&path=${encodeURIComponent(path)}`;

     export const getSftpUploadUrl = () =>
         `http://${window.location.hostname}:20180/api/sftp/upload`;

     // WebSocket 连接地址
     export const getSSHWebSocketUrl = (sessionId: string, w: number, h: 
     number) =>
         `ws://${window.location.hostname}:20180/api/ssh/conn?session_id=${s
     essionId}&w=${w}&h=${h}`;

     // ========== 会话管理 ==========
     export const getOnlineSessions = () =>
         api.post('/ssh/GetOnlineSessions', {});

     export const forceDisconnect = (sessionId: string) =>
         api.post('/ssh/ForceDisconnect', { session_id: sessionId });

     3.4 页面设计

     页面 1：主机管理（ssh-hosts.vue）

     - 主机列表表格（Arco
     Table），字段：名称、地址、端口、用户、认证方式、操作
     - 操作列：编辑、删除、快速连接（跳转 SSH 终端页）
     - 新增/编辑弹窗（Arco Modal + Form）：
       - 基本信息：名称、地址、端口、用户名
       - 认证方式切换（密码 / 证书）
       - 终端外观配置（字体大小、颜色选择器、字体样式下拉）
       - 高级配置折叠：Shell、PtyType、初始命令

     页面 2：SSH 终端（ssh-terminal.vue）- 重写

     - 顶部：Tab 栏（支持多终端标签页，每个标签对应一个 SSH 连接）
     - 每个标签内容：
       - 使用 @xterm/xterm + @xterm/addon-attach（WebSocket 直接绑定到
     xterm）+ @xterm/addon-fit（自动适配尺寸）
       - 工具栏：重连、清屏、断开、文件管理（跳转 SFTP 页）、终端设置
     - 连接流程：
       a. 调用 createSSHSession({ host_id }) 获取 session_id
       b. 构建 WebSocket URL，new Terminal() + new AttachAddon(ws)
       c. 监听 ws close 事件，自动更新状态
     - 支持从主机列表页跳转过来，通过 route.query.hostId 自动连接

     页面 3：SFTP 文件管理（ssh-file-manager.vue）

     - 左侧：面包屑路径导航 + 路径手动输入框
     - 主体：文件/目录列表表格（名称、类型、大小、修改时间、权限、操作）
     - 操作：
       - 上传按钮（Arco Upload，自定义 action 指向 SSH 独立端口）
       - 下载按钮（创建 <a> 标签触发下载）
       - 新建目录按钮
       - 删除按钮（带确认）
     - 依赖当前活跃的 session_id，可从 SSH 终端页跳转过来

     页面 4：会话管理（ssh-sessions.vue）

     - 在线会话列表表格：Session ID、主机地址、用户、客户端
     IP、连接时间、最后活跃时间
     - 操作：强制断开

     3.5 前端依赖安装

     cd web && pnpm add @xterm/xterm @xterm/addon-attach @xterm/addon-fit

     ---
     四、关键实现细节

     4.1 WebSocket 保持 SSH 独立端口

     Gateway（httptest.NewRecorder）无法代理 WebSocket。因此：
     - REST API 走 Gateway（端口 20103）
     - WebSocket + 文件上传下载走 SSH 独立 HTTP 服务（端口 20180）

     前端通过 window.location.hostname + 配置端口号构建 URL。

     4.2 敏感数据加密

     复用 server/internal/common/crypto/ 中的 AES 工具：
     - DAO 层 Create/Update 前加密 password、cert_data、cert_pwd
     - DAO 层 Find 后解密
     - API 响应中 password 字段统一不返回明文（返回空字符串或 ***）

     4.3 会话管理器

     // conn/session_manager.go
     type SessionManager struct {
         clients sync.Map  // sessionID -> *SSHConn
     }

     func (m *SessionManager) Store(sessionID string, conn *SSHConn)
     func (m *SessionManager) Load(sessionID string) (*SSHConn, bool)
     func (m *SessionManager) Delete(sessionID string)
     func (m *SessionManager) Range(fn func(sessionID string, conn *SSHConn)
      bool)
     func (m *SessionManager) StartCleanupTimer(interval time.Duration, 
     maxIdle time.Duration)

     4.4 xterm.js 与 WebSocket 集成模式

     // ssh-terminal.vue 核心逻辑
     const initTerminal = (sessionId: string, config: TerminalConfig) => {
         const term = new Terminal({
             cursorBlink: true,
             fontSize: config.font_size,
             fontFamily: config.font_family,
             cursorStyle: config.cursor_style,
             theme: {
                 background: config.background,
                 foreground: config.foreground,
                 cursor: config.cursor_color,
             },
         });

         const fitAddon = new FitAddon();
         term.loadAddon(fitAddon);
         term.open(terminalRef.value);
         fitAddon.fit();

         const wsUrl = getSSHWebSocketUrl(sessionId, term.cols, term.rows);
         const ws = new WebSocket(wsUrl);
         const attachAddon = new AttachAddon(ws);
         term.loadAddon(attachAddon);

         // 窗口 resize 时同步调整
         const resizeObserver = new ResizeObserver(() => {
             fitAddon.fit();
             resizeSSHTerminal(sessionId, term.cols, term.rows);
         });
         resizeObserver.observe(terminalRef.value);

         ws.onclose = () => { /* 更新连接状态 */ };
     };

     ---
     五、实施步骤

     Step 1: 后端 model + dao

     - 创建 model/ssh_host.go、model/ssh_session.go
     - 创建 dao/ssh_host_dao.go、dao/ssh_session_dao.go
     - 在 database/manager.go 的 AutoMigrate 中注册新表

     Step 2: 后端 conn 层

     - 重构 conn/ssh_conn.go（从旧 app/service/ssh_conn.go 改造）
     - 重构 conn/session_manager.go（从旧 app/service/service_init.go 改造）
     - 保留 ssh/crypto/、ssh/sftp/ 依赖

     Step 3: 后端 service + api

     - 定义 service.go 接口
     - 实现 impl.go
     - 实现 api/router.go、各 handler 文件
     - 统一使用
     common.SuccessResponse、common.HandleAppError、errors.AppError

     Step 4: 后端 gateway + bootstrap

     - 实现 gateway/handler.go（对齐 cloudnode 模式）
     - 修改 bootstrap/services.go，注册 SSH 服务
     - 修改 gateway/init.go 注册 SSHGatewayHandler

     Step 5: 前端依赖 + API

     - 安装 xterm 相关 npm 包
     - 创建 api/modules/ssh.ts
     - 清理旧的 api/modules/container.ts 中 SSH 相关内容

     Step 6: 前端页面

     - 新增 ssh-hosts.vue、ssh-file-manager.vue、ssh-sessions.vue
     - 重写 ssh-terminal.vue（xterm.js + 真实 WebSocket）
     - 更新 router/route.ts 新增路由

     Step 7: 前端菜单

     - 在菜单配置中新增「SSH管理」子菜单：主机管理、SSH终端、文件管理、会话
     管理

     ---
     六、验证方案

     1. 后端编译验证：cd server && go build ./...
     2. 数据库迁移验证：启动服务后检查 SQLite 中是否自动创建 t_ssh_host 和
     t_ssh_session 表
     3. API 联调验证：
       - 通过 Postman/curl 测试主机 CRUD（走 Gateway）
       - 测试 SSH 会话创建 → WebSocket 连接 → 终端交互
       - 测试 SFTP 目录浏览 / 上传 / 下载 / 删除
     4. 前端验证：
       - 主机管理页：新增 / 编辑 / 删除主机
       - SSH 终端页：选择主机 → 连接 → 终端输入输出 → 重连 → 清屏
       - 文件管理页：浏览目录 / 上传 / 下载 / 创建目录 / 删除
       - 会话管理页：查看在线列表 / 强制断开
