
-- ============ MooX Admin 本地基础数据库表设计 ============

PRAGMA foreign_keys = ON;

DROP TABLE IF EXISTS t_user_actions;

-- ************ 平台空间表 ************
CREATE TABLE IF NOT EXISTS t_spaces (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    c_owner TEXT NOT NULL DEFAULT '',
    c_market TEXT NOT NULL DEFAULT '',
    c_timezone TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attributes TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_spaces_space_id_deleted
ON t_spaces(c_space_id, c_is_deleted);

CREATE TABLE IF NOT EXISTS t_space_members (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL,
    c_user_id TEXT NOT NULL,
    c_role TEXT NOT NULL DEFAULT 'member',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_attributes TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_space_members_space_user
ON t_space_members(c_space_id, c_user_id);
CREATE INDEX IF NOT EXISTS idx_space_members_user_id
ON t_space_members(c_user_id);

-- ************ 系统服务部署信息表 ************
CREATE TABLE IF NOT EXISTS t_service_deployments (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_service_name TEXT NOT NULL,
    c_service_kind TEXT NOT NULL DEFAULT '',
    c_protocol TEXT NOT NULL DEFAULT 'http',
    c_host TEXT NOT NULL DEFAULT '',
    c_port INTEGER NOT NULL DEFAULT 0,
    c_base_url TEXT NOT NULL DEFAULT '',
    c_rpc_address TEXT NOT NULL DEFAULT '',
    c_gateway_path TEXT NOT NULL DEFAULT '',
    c_scope TEXT NOT NULL DEFAULT 'public',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_description TEXT NOT NULL DEFAULT '',
    c_extra_config TEXT NOT NULL DEFAULT '{}',
    c_is_deleted INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_service_deployments_name_deleted
ON t_service_deployments(c_service_name, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_service_deployments_kind
ON t_service_deployments(c_service_kind);
CREATE INDEX IF NOT EXISTS idx_service_deployments_scope
ON t_service_deployments(c_scope);
CREATE INDEX IF NOT EXISTS idx_service_deployments_status
ON t_service_deployments(c_status);

DROP TRIGGER IF EXISTS update_service_deployments_mtime;
CREATE TRIGGER IF NOT EXISTS update_service_deployments_mtime AFTER UPDATE ON t_service_deployments BEGIN
    UPDATE t_service_deployments SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ************ 用户表 ************
CREATE TABLE IF NOT EXISTS t_users (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_user_id TEXT NOT NULL,                                   -- 用户UUID (对应代码中的string类型)
    c_username TEXT NOT NULL,                                  -- 用户名
    c_password_hash TEXT NOT NULL,                             -- 密码哈希
    c_nickname TEXT DEFAULT '',                                -- 昵称 
    c_email TEXT DEFAULT '',                                   -- 邮箱 
    c_avatar TEXT DEFAULT '',                                  -- 头像URL 
    c_role INTEGER NOT NULL DEFAULT 1,                        -- 用户角色: 0-GUEST, 1-USER, 2-ADMIN, 3-SUPER_ADMIN
    c_status INTEGER NOT NULL DEFAULT 1,                      -- 用户状态: 0-INACTIVE, 1-ACTIVE, 2-SUSPENDED, 3-BANNED
    c_last_login_at DATETIME,                                  -- 最后登录时间 
    c_last_login_ip TEXT DEFAULT '',                           -- 最后登录IP 
    c_last_password_change DATETIME DEFAULT CURRENT_TIMESTAMP, -- 最后密码修改时间
    c_is_deleted INTEGER NOT NULL DEFAULT 0,                    -- 删除标记: 0=有效,1=删除
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,               -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                -- 修改时间
);

-- ************ 登录历史表 (安全审计) ************
CREATE TABLE IF NOT EXISTS t_login_history (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_user_id TEXT NOT NULL,                                   -- 用户UUID
    c_username TEXT NOT NULL,                                  -- 用户名 (冗余存储，便于查询)
    c_login_type TEXT NOT NULL DEFAULT 'password',             -- 登录类型: password, sms, third_party
    c_client_ip TEXT NOT NULL,                                 -- 客户端IP
    c_user_agent TEXT DEFAULT '',                              -- 用户代理
    c_device_id TEXT DEFAULT '',                               -- 设备ID
    c_location TEXT DEFAULT '',                                -- 登录地理位置 (可选)
    c_login_result TEXT NOT NULL,                              -- 登录结果: success, failed, locked
    c_failure_reason TEXT DEFAULT '',                          -- 失败原因
    c_session_duration INTEGER DEFAULT 0,                     -- 会话时长(秒) (登出时更新)
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,               -- 登录时间
    
    FOREIGN KEY (c_user_id) REFERENCES t_users(c_user_id) ON DELETE CASCADE
);

-- ************ 创建索引 ************
-- 用户表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_user_id ON t_users(c_user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON t_users(c_username);
CREATE INDEX IF NOT EXISTS idx_users_email ON t_users(c_email);
CREATE INDEX IF NOT EXISTS idx_users_status ON t_users(c_status);
CREATE INDEX IF NOT EXISTS idx_users_role ON t_users(c_role);
CREATE INDEX IF NOT EXISTS idx_users_last_login ON t_users(c_last_login_at);

-- 登录历史索引
CREATE INDEX IF NOT EXISTS idx_login_history_user_id ON t_login_history(c_user_id);
CREATE INDEX IF NOT EXISTS idx_login_history_ip ON t_login_history(c_client_ip);
CREATE INDEX IF NOT EXISTS idx_login_history_time ON t_login_history(c_ctime);
CREATE INDEX IF NOT EXISTS idx_login_history_result ON t_login_history(c_login_result);

-- ************ 创建触发器，自动更新修改时间 ************
-- 用户表触发器 - 更新时间
CREATE TRIGGER IF NOT EXISTS update_users_mtime AFTER UPDATE ON t_users BEGIN 
    UPDATE t_users SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ SSH 主机管理系统表设计 ============

-- ************ SSH 主机配置表 ************
CREATE TABLE IF NOT EXISTS t_ssh_host (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 主键ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_name TEXT NOT NULL,                                       -- 主机名称
    c_address TEXT NOT NULL,                                    -- 主机地址（IP或域名）
    c_port INTEGER NOT NULL DEFAULT 22,                        -- SSH端口
    c_user TEXT NOT NULL,                                       -- 登录用户名
    c_password TEXT DEFAULT '',                                 -- 登录密码（AES加密存储）
    c_auth_type TEXT NOT NULL DEFAULT 'pwd',                   -- 认证方式: pwd=密码, cert=证书
    c_net_type TEXT NOT NULL DEFAULT 'tcp4',                   -- 网络类型: tcp4=IPv4, tcp6=IPv6
    c_cert_data TEXT,                                           -- 证书数据（AES加密存储）
    c_cert_pwd TEXT DEFAULT '',                                 -- 证书密码（AES加密存储）
    -- 终端外观配置
    c_font_size INTEGER NOT NULL DEFAULT 14,                   -- 终端字体大小
    c_background TEXT NOT NULL DEFAULT '#000000',               -- 终端背景色
    c_foreground TEXT NOT NULL DEFAULT '#FFFFFF',               -- 终端前景色
    c_cursor_color TEXT NOT NULL DEFAULT '#FFFFFF',             -- 光标颜色
    c_font_family TEXT NOT NULL DEFAULT 'Courier New',         -- 字体
    c_cursor_style TEXT NOT NULL DEFAULT 'block',              -- 光标样式: block/underline/bar
    -- Shell 配置
    c_shell TEXT NOT NULL DEFAULT 'bash',                      -- Shell类型
    c_pty_type TEXT NOT NULL DEFAULT 'xterm-256color',         -- 终端类型
    c_init_cmd TEXT,                                            -- 连接后初始命令
    -- 元数据
    c_creator TEXT NOT NULL DEFAULT '',                         -- 创建人
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

-- ************ SSH 会话表（用于会话管理） ************
CREATE TABLE IF NOT EXISTS t_ssh_session (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 主键ID
    c_space_id TEXT NOT NULL DEFAULT '',                       -- 空间ID
    c_session_id TEXT NOT NULL,                                 -- 会话唯一标识
    c_host_id INTEGER NOT NULL,                                -- 关联主机ID
    c_host_name TEXT DEFAULT '',                                -- 主机名称（冗余）
    c_host_address TEXT NOT NULL,                               -- 主机地址（冗余）
    c_client_ip TEXT DEFAULT '',                                -- 客户端IP
    c_username TEXT DEFAULT '',                                 -- 登录用户名
    c_status TEXT NOT NULL DEFAULT 'connected',                -- 状态: connected/disconnected/error
    c_connect_time DATETIME,                                    -- 连接时间
    c_close_time DATETIME,                                      -- 关闭时间
    c_error_msg TEXT                                              -- 错误信息
);

-- ************ 创建SSH相关索引 ************
CREATE INDEX IF NOT EXISTS idx_ssh_host_space_id ON t_ssh_host(c_space_id);
CREATE INDEX IF NOT EXISTS idx_ssh_host_name ON t_ssh_host(c_name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_host_address ON t_ssh_host(c_address);
CREATE INDEX IF NOT EXISTS idx_ssh_host_mtime ON t_ssh_host(c_mtime);

CREATE INDEX IF NOT EXISTS idx_ssh_session_space_id ON t_ssh_session(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_session_session_id ON t_ssh_session(c_session_id);
CREATE INDEX IF NOT EXISTS idx_ssh_session_host_id ON t_ssh_session(c_host_id);
CREATE INDEX IF NOT EXISTS idx_ssh_session_status ON t_ssh_session(c_status);
CREATE INDEX IF NOT EXISTS idx_ssh_session_connect_time ON t_ssh_session(c_connect_time);

-- ************ 创建SSH相关触发器 ************
DROP TRIGGER IF EXISTS update_ssh_host_mtime;
CREATE TRIGGER IF NOT EXISTS update_ssh_host_mtime AFTER UPDATE ON t_ssh_host BEGIN
    UPDATE t_ssh_host SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ 秘钥管理系统表设计 ============

-- ************ 秘钥管理表 ************
CREATE TABLE IF NOT EXISTS t_secrets (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 主键ID
    c_space_id TEXT NOT NULL DEFAULT '',                        -- 空间ID
    c_secret_id TEXT NOT NULL,                                  -- 秘钥唯一标识 (UUID)
    c_name TEXT NOT NULL,                                       -- 秘钥名称
    c_description TEXT NOT NULL DEFAULT '',                     -- 秘钥描述/备注
    c_category TEXT NOT NULL,                                   -- 秘钥分类: ssh=SSH凭证, exchange=交易所,
                                                                --           database=数据库,
                                                                --           jwt=系统令牌, other=其他
    c_provider TEXT NOT NULL DEFAULT '',                        -- 提供方/来源 (binance/okx/mysql/postgres等)
    c_secret_type TEXT NOT NULL DEFAULT 'api_key',              -- 秘钥类型: api_key=API密钥对, password=密码,
                                                                --           token=访问令牌, certificate=证书,
                                                                --           ssh_key=SSH密钥, other=其他
    c_key_id TEXT NOT NULL DEFAULT '',                          -- 公开标识 (SecretId/AppId/Username/API Key)，不脱敏
    c_secret_value TEXT NOT NULL,                               -- 秘钥值（AES加密存储，返回时脱敏）
    c_extra_config TEXT NOT NULL DEFAULT '{}',                  -- 额外配置 (JSON: cert_pwd/passphrase/region/permissions等)
    c_status TEXT NOT NULL DEFAULT 'active',                    -- 状态: active=启用, inactive=禁用
    c_last_used_at DATETIME,                                    -- 最后使用时间
    c_last_used_by TEXT NOT NULL DEFAULT '',                    -- 最后使用方（服务/模块名）
    c_creator TEXT NOT NULL DEFAULT '',                         -- 创建人
    c_is_deleted INTEGER NOT NULL DEFAULT 0,                    -- 软删除标记: 0=有效,1=删除
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                 -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                  -- 修改时间
);

-- ************ 秘钥管理相关索引 ************
CREATE INDEX IF NOT EXISTS idx_secrets_space_id ON t_secrets(c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_secret_id_deleted ON t_secrets(c_secret_id, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_secrets_category ON t_secrets(c_category);
CREATE INDEX IF NOT EXISTS idx_secrets_provider ON t_secrets(c_provider);
CREATE INDEX IF NOT EXISTS idx_secrets_status ON t_secrets(c_status);
CREATE INDEX IF NOT EXISTS idx_secrets_name ON t_secrets(c_name);
CREATE INDEX IF NOT EXISTS idx_secrets_deleted ON t_secrets(c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_secrets_ctime ON t_secrets(c_ctime DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_eventbus_active_key
    ON t_secrets(c_space_id, c_category, c_provider, c_key_id)
    WHERE c_is_deleted = 0 AND c_status = 'active' AND c_category = 'eventbus';

-- ************ 秘钥管理触发器 ************
DROP TRIGGER IF EXISTS update_secrets_mtime;
CREATE TRIGGER IF NOT EXISTS update_secrets_mtime AFTER UPDATE ON t_secrets BEGIN
    UPDATE t_secrets SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;
