-- MooX Admin 本地基础数据库表设计

PRAGMA foreign_keys = ON;

-- 用户表：系统只有一个超级用户，登录记录写入全局活动日志（auth.login）
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

CREATE TRIGGER IF NOT EXISTS trg_t_users_mtime
AFTER UPDATE ON t_users
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_users SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 主机登记表：唯一的主机来源，只能通过 moox-cli 维护
-- 部署配置存 t_deployments，网关运行态保存在 Admin 进程内存中；SSH 的端口、用户和指纹是本表的列，密码或私钥存在 t_secrets（moox:ssh-host:<主机ID>）
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
    -- 1：这台主机的网关开外部入口（11004），外部身份从这里进来
    c_external_entry INTEGER NOT NULL DEFAULT 0,
    c_description TEXT NOT NULL DEFAULT '',
    -- 0 表示控制台不能 SSH 到这台主机
    c_ssh_port INTEGER NOT NULL DEFAULT 0,
    c_ssh_user TEXT NOT NULL DEFAULT '',
    -- 首次连接记录的主机密钥指纹，格式为“地址:端口 指纹”，地址或端口变化后自然失效
    c_ssh_fingerprint TEXT NOT NULL DEFAULT '',
    -- 这台主机的签名公钥（base64 的 32 字节 ed25519 公钥），本机全部组件共用；空表示还没签发过身份包，私钥只在主机上
    c_sign_public_key TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('enabled', 'disabled')),
    CHECK (c_reachability IN ('direct', 'outbound')),
    CHECK (c_external_entry IN (0, 1)),
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

-- 不属于主机的签名身份的公钥：moox-cli、scf-collector；一个身份一把，没有新旧并存
CREATE TABLE IF NOT EXISTS t_identity_keys (
    c_identity TEXT NOT NULL PRIMARY KEY,
    c_sign_public_key TEXT NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS t_deployments (
    c_host_id TEXT NOT NULL,
    c_component_id TEXT NOT NULL,
    c_status TEXT NOT NULL DEFAULT 'enabled',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_status IN ('enabled', 'disabled')),
    FOREIGN KEY (c_host_id) REFERENCES t_hosts (c_host_id) ON DELETE CASCADE,
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

-- 秘钥管理表：物理硬删除，c_secret_value 与 c_extra_config 均使用 AES-GCM 加密存储
CREATE TABLE IF NOT EXISTS t_secrets (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_secret_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_description TEXT NOT NULL DEFAULT '',
    -- 秘钥分类：ssh=SSH凭证, exchange=交易所, cloud=云厂商凭据, database=数据库, jwt=系统令牌, eventbus=事件总线, other=其他
    c_category TEXT NOT NULL,
    -- 提供方/来源（binance/okx/tencent/moox_eventbus 等）
    c_provider TEXT NOT NULL DEFAULT '',
    -- 秘钥类型：api_key/password/token/certificate/ssh_key/other
    c_secret_type TEXT NOT NULL DEFAULT 'api_key',
    -- 公开标识（SecretId/AppId/Username/API Key），不脱敏
    c_key_id TEXT NOT NULL DEFAULT '',
    -- 秘钥值（AES-GCM 加密存储，返回时脱敏）
    c_secret_value TEXT NOT NULL,
    -- 额外配置 JSON（AES-GCM 加密存储，含 passphrase 等敏感项）
    c_extra_config TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_creator TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_secret_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_eventbus_active_key
ON t_secrets (c_category, c_provider, c_key_id)
WHERE c_status = 'active' AND c_category = 'eventbus';

CREATE TRIGGER IF NOT EXISTS trg_t_secrets_mtime
AFTER UPDATE ON t_secrets
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_secrets SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;

-- 活动日志：各模块写入的、需要展示给用户的事件；永久保存，不清理；c_ctime 为 Unix 毫秒时间戳
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
    c_ctime INTEGER NOT NULL,
    CHECK (c_level IN ('info', 'warning', 'error'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uk_t_activity_logs_dedup
ON t_activity_logs (c_dedup_key)
WHERE c_dedup_key <> '';
CREATE INDEX IF NOT EXISTS idx_t_activity_logs_space_time ON t_activity_logs (c_space_id, c_ctime);
CREATE INDEX IF NOT EXISTS idx_t_activity_logs_category_time ON t_activity_logs (c_category, c_ctime);
