-- ============ MooX Admin 本地基础数据库表设计 ============

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS t_collector_publish_leases (
    c_space_id TEXT NOT NULL PRIMARY KEY,
    c_lease_id TEXT NOT NULL DEFAULT '',
    c_holder_id TEXT NOT NULL DEFAULT '',
    c_fencing_token INTEGER NOT NULL DEFAULT 0,
    c_expires_at_unix_ms INTEGER NOT NULL DEFAULT 0,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_fencing_token >= 0)
);

CREATE TABLE IF NOT EXISTS t_collector_publish_operations (
    c_space_id TEXT NOT NULL,
    c_operation_id TEXT NOT NULL,
    c_lease_id TEXT NOT NULL,
    c_fencing_token INTEGER NOT NULL,
    c_expires_at_unix_ms INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_operation_id),
    CHECK (c_fencing_token > 0)
);

CREATE INDEX IF NOT EXISTS idx_collector_publish_operations_expiry
ON t_collector_publish_operations (c_space_id, c_expires_at_unix_ms);

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
ON t_spaces (c_space_id, c_is_deleted);

-- ************ 用户表 ************
-- 系统只有一个超级用户：没有角色、状态和个人资料；登录记录写入全局活动日志（auth.login）。
CREATE TABLE IF NOT EXISTS t_users (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_user_id TEXT NOT NULL,
    c_username TEXT NOT NULL,
    c_password_hash TEXT NOT NULL,
    c_last_login_at DATETIME,
    c_last_login_ip TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_user_id ON t_users (c_user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON t_users (c_username);

CREATE TRIGGER IF NOT EXISTS update_users_mtime
AFTER UPDATE ON t_users
BEGIN
    UPDATE t_users SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

-- ============ 主机登记与部署 ============

-- 主机登记表：唯一的主机来源，只能通过 moox-cli 维护。
-- 部署配置与网关运行状态分表；SSH 的端口、用户和指纹是本表的列，密码或私钥存在 t_secrets（moox:ssh-host:<主机ID>）。
CREATE TABLE IF NOT EXISTS t_hosts (
    c_host_id TEXT NOT NULL PRIMARY KEY,
    -- 控制台展示用的中文名，如“控制机”
    c_label TEXT NOT NULL DEFAULT '',
    c_address TEXT NOT NULL,
    c_private_address TEXT NOT NULL DEFAULT '',
    c_region TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'enabled',
    -- direct：可直达；outbound：只能出站，经 control 网关反向连接中转
    c_reachability TEXT NOT NULL DEFAULT 'direct',
    c_description TEXT NOT NULL DEFAULT '',
    -- 0 表示控制台不能 SSH 到这台主机
    c_ssh_port INTEGER NOT NULL DEFAULT 0,
    c_ssh_user TEXT NOT NULL DEFAULT '',
    -- 首次连接记录的主机密钥指纹，格式为“地址:端口 指纹”，地址或端口变化后自然失效
    c_ssh_fingerprint TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('enabled', 'disabled')),
    CHECK (c_reachability IN ('direct', 'outbound')),
    CHECK (c_ssh_port BETWEEN 0 AND 65535),
    UNIQUE (c_address)
);

CREATE TRIGGER IF NOT EXISTS trg_t_hosts_mtime
AFTER UPDATE ON t_hosts
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_hosts SET c_mtime = CURRENT_TIMESTAMP WHERE c_host_id = OLD.c_host_id;
END;

CREATE TABLE IF NOT EXISTS t_deployments (
    c_host_id TEXT NOT NULL,
    c_component_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'enabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_host_id) REFERENCES t_hosts (c_host_id),
    PRIMARY KEY (c_host_id, c_component_id)
);

CREATE INDEX IF NOT EXISTS idx_t_deployments_component ON t_deployments (c_component_id);

CREATE TRIGGER IF NOT EXISTS trg_t_deployments_mtime
AFTER UPDATE ON t_deployments
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_deployments SET c_mtime = CURRENT_TIMESTAMP
    WHERE c_host_id = OLD.c_host_id AND c_component_id = OLD.c_component_id;
END;

CREATE TABLE IF NOT EXISTS t_host_gateway_status (
    c_host_id TEXT NOT NULL PRIMARY KEY,
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_version TEXT NOT NULL DEFAULT '',
    c_expected_hash TEXT NOT NULL DEFAULT '',
    c_applied_hash TEXT NOT NULL DEFAULT '',
    c_route_count INTEGER NOT NULL DEFAULT 0,
    c_last_seen_at DATETIME,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_previous_instance_id TEXT NOT NULL DEFAULT '',
    c_replaced_at DATETIME,
    c_conflict_instance_id TEXT NOT NULL DEFAULT '',
    c_conflict_seen_at DATETIME,
    -- control 网关上报的反向连接会话（JSON 数组），其他主机为空
    c_relay_sessions TEXT NOT NULL DEFAULT '',
    CHECK (c_route_count >= 0),
    FOREIGN KEY (c_host_id) REFERENCES t_hosts (c_host_id)
);

-- ============ 秘钥管理系统表设计 ============

-- ************ 秘钥管理表 ************
CREATE TABLE IF NOT EXISTS t_secrets (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 主键ID
    c_space_id TEXT NOT NULL DEFAULT '',                        -- 空间ID
    c_secret_id TEXT NOT NULL,                                  -- 秘钥唯一标识 (UUID)
    c_name TEXT NOT NULL,                                       -- 秘钥名称
    c_description TEXT NOT NULL DEFAULT '',                     -- 秘钥描述/备注
    c_category TEXT NOT NULL,                                   -- 秘钥分类: ssh=SSH凭证, exchange=交易所,
                                                                --           cloud=云厂商凭据, database=数据库,
                                                                --           jwt=系统令牌, other=其他
    c_provider TEXT NOT NULL DEFAULT '',                        -- 提供方/来源 (binance/okx/mysql/postgres等)
    c_secret_type TEXT NOT NULL DEFAULT 'api_key',              -- 秘钥类型: api_key=API密钥对, password=密码,
                                                                --           token=访问令牌, certificate=证书,
                                                                --           ssh_key=SSH密钥, other=其他
    c_key_id TEXT NOT NULL DEFAULT '',                          -- 公开标识 (SecretId/AppId/Username/API Key)，不脱敏
    c_secret_value TEXT NOT NULL,                               -- 秘钥值（AES加密存储，返回时脱敏）
    c_extra_config TEXT NOT NULL DEFAULT '{}',                  -- 额外配置 (JSON: cert_pwd/passphrase/region/permissions等)
    c_status TEXT NOT NULL DEFAULT 'active',                    -- 状态: active=启用, inactive=禁用
    c_creator TEXT NOT NULL DEFAULT '',                         -- 创建人
    c_is_deleted INTEGER NOT NULL DEFAULT 0,                    -- 软删除标记: 0=有效,1=删除
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                 -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                  -- 修改时间
);

-- ************ 秘钥管理相关索引 ************
CREATE INDEX IF NOT EXISTS idx_secrets_space_id ON t_secrets (c_space_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_secret_id_deleted ON t_secrets (c_secret_id, c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_secrets_category ON t_secrets (c_category);
CREATE INDEX IF NOT EXISTS idx_secrets_provider ON t_secrets (c_provider);
CREATE INDEX IF NOT EXISTS idx_secrets_status ON t_secrets (c_status);
CREATE INDEX IF NOT EXISTS idx_secrets_name ON t_secrets (c_name);
CREATE INDEX IF NOT EXISTS idx_secrets_deleted ON t_secrets (c_is_deleted);
CREATE INDEX IF NOT EXISTS idx_secrets_ctime ON t_secrets (c_ctime DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_eventbus_active_key
ON t_secrets (c_space_id, c_category, c_provider, c_key_id)
WHERE c_is_deleted = 0 AND c_status = 'active' AND c_category = 'eventbus';

-- ************ 秘钥管理触发器 ************
DROP TRIGGER IF EXISTS update_secrets_mtime;
CREATE TRIGGER IF NOT EXISTS update_secrets_mtime
AFTER UPDATE ON t_secrets
BEGIN
    UPDATE t_secrets SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

-- 活动日志：各模块写入的、需要展示给用户的事件；永久保存，不清理
CREATE TABLE IF NOT EXISTS t_activity_logs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_source TEXT NOT NULL,
    c_category TEXT NOT NULL,
    c_level TEXT NOT NULL DEFAULT 'info',
    c_title TEXT NOT NULL,
    c_detail TEXT NOT NULL DEFAULT '{}',
    c_ref_id TEXT NOT NULL DEFAULT '',
    c_dedup_key TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_level IN ('info', 'warning', 'error'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_t_activity_logs_dedup
ON t_activity_logs (c_dedup_key)
WHERE c_dedup_key <> '';
CREATE INDEX IF NOT EXISTS idx_t_activity_logs_space_time ON t_activity_logs (c_space_id, c_ctime);
CREATE INDEX IF NOT EXISTS idx_t_activity_logs_category_time ON t_activity_logs (c_category, c_ctime);
