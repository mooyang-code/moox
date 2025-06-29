
-- ============ Moox 认证系统数据库表设计 ============

-- ************ 用户表 ************
CREATE TABLE t_users (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_user_id TEXT NOT NULL,                                   -- 用户UUID (对应代码中的string类型)
    c_username TEXT NOT NULL,                                  -- 用户名
    c_password_hash TEXT NOT NULL,                             -- 密码哈希
    c_salt TEXT NOT NULL,                                      -- 密码盐值 
    c_nickname TEXT DEFAULT '',                                -- 昵称 
    c_email TEXT DEFAULT '',                                   -- 邮箱 
    c_avatar TEXT DEFAULT '',                                  -- 头像URL 
    c_role INTEGER NOT NULL DEFAULT 1,                        -- 用户角色: 0-GUEST, 1-USER, 2-ADMIN, 3-SUPER_ADMIN
    c_status INTEGER NOT NULL DEFAULT 1,                      -- 用户状态: 0-INACTIVE, 1-ACTIVE, 2-SUSPENDED, 3-BANNED
    c_last_login_at DATETIME,                                  -- 最后登录时间 
    c_last_login_ip TEXT DEFAULT '',                           -- 最后登录IP 
    c_last_password_change DATETIME DEFAULT CURRENT_TIMESTAMP, -- 最后密码修改时间
    c_login_attempts INTEGER DEFAULT 0,                       -- 登录尝试次数 (用于安全控制)
    c_locked_until DATETIME,                                   -- 锁定到期时间 
    c_invalid INTEGER NOT NULL DEFAULT 0,                     -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,               -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,               -- 修改时间
    
    UNIQUE (c_user_id),
    UNIQUE (c_username)
);

-- ************ 活跃令牌表 (JWT会话管理) ************
CREATE TABLE t_active_tokens (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_jti TEXT NOT NULL,                                       -- JWT ID (唯一标识)
    c_user_id TEXT NOT NULL,                                   -- 用户UUID (对应用户表)
    c_token_type TEXT NOT NULL DEFAULT 'access',               -- 令牌类型: access, refresh 
    c_device_id TEXT DEFAULT '',                               -- 设备ID (对应代码中的设备识别)
    c_user_agent TEXT DEFAULT '',                              -- 用户代理信息 
    c_client_ip TEXT DEFAULT '',                               -- 客户端IP 
    c_issued_at DATETIME NOT NULL,                             -- 签发时间 
    c_expires_at DATETIME NOT NULL,                            -- 过期时间
    c_last_used_at DATETIME DEFAULT CURRENT_TIMESTAMP,        -- 最后使用时间 
    c_revoked INTEGER NOT NULL DEFAULT 0,                     -- 是否已撤销 (用于主动登出)
    c_invalid INTEGER NOT NULL DEFAULT 0,                     -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,               -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,               -- 修改时间
    
    UNIQUE (c_jti),
    FOREIGN KEY (c_user_id) REFERENCES t_users(c_user_id) ON DELETE CASCADE
);

-- ************ 登录历史表 (安全审计) ************
CREATE TABLE t_login_history (
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

-- ************ 用户操作日志表 (可选，用于审计) ************
CREATE TABLE t_user_actions (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 自增ID
    c_user_id TEXT NOT NULL,                                   -- 用户UUID
    c_action TEXT NOT NULL,                                    -- 操作类型: login, logout, change_password, update_profile
    c_resource TEXT DEFAULT '',                                -- 操作资源
    c_details TEXT DEFAULT '',                                 -- 操作详情 (JSON格式)
    c_client_ip TEXT DEFAULT '',                               -- 客户端IP
    c_user_agent TEXT DEFAULT '',                              -- 用户代理
    c_result TEXT NOT NULL DEFAULT 'success',                 -- 操作结果: success, failed
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,               -- 操作时间
    
    FOREIGN KEY (c_user_id) REFERENCES t_users(c_user_id) ON DELETE CASCADE
);

-- ************ 创建索引 ************
-- 用户表索引
CREATE INDEX idx_users_username ON t_users(c_username);
CREATE INDEX idx_users_email ON t_users(c_email);
CREATE INDEX idx_users_status ON t_users(c_status);
CREATE INDEX idx_users_role ON t_users(c_role);
CREATE INDEX idx_users_last_login ON t_users(c_last_login_at);

-- 令牌表索引
CREATE INDEX idx_tokens_user_id ON t_active_tokens(c_user_id);
CREATE INDEX idx_tokens_expires ON t_active_tokens(c_expires_at);
CREATE INDEX idx_tokens_type ON t_active_tokens(c_token_type);
CREATE INDEX idx_tokens_device ON t_active_tokens(c_device_id);
CREATE INDEX idx_tokens_revoked ON t_active_tokens(c_revoked);

-- 登录历史索引
CREATE INDEX idx_login_history_user_id ON t_login_history(c_user_id);
CREATE INDEX idx_login_history_ip ON t_login_history(c_client_ip);
CREATE INDEX idx_login_history_time ON t_login_history(c_ctime);
CREATE INDEX idx_login_history_result ON t_login_history(c_login_result);

-- 操作日志索引
CREATE INDEX idx_user_actions_user_id ON t_user_actions(c_user_id);
CREATE INDEX idx_user_actions_action ON t_user_actions(c_action);
CREATE INDEX idx_user_actions_time ON t_user_actions(c_ctime);

-- ************ 创建触发器，自动更新修改时间 ************
-- 用户表触发器 - 更新时间
CREATE TRIGGER update_users_mtime AFTER UPDATE ON t_users
BEGIN
    UPDATE t_users SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

-- 活跃令牌表触发器 - 更新时间
CREATE TRIGGER update_tokens_mtime AFTER UPDATE ON t_active_tokens
BEGIN
    UPDATE t_active_tokens SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

