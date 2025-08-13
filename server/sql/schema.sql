
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
CREATE TRIGGER update_users_mtime AFTER UPDATE ON t_users BEGIN UPDATE t_users SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 活跃令牌表触发器 - 更新时间
CREATE TRIGGER update_tokens_mtime AFTER UPDATE ON t_active_tokens BEGIN UPDATE t_active_tokens SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;


-- ============ 云函数数据采集器系统表设计 ============

-- ************ 云账户配置表 ************
CREATE TABLE IF NOT EXISTS t_cloud_accounts (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_account_id TEXT NOT NULL, -- 账户唯一标识
    c_account_name TEXT NOT NULL, -- 账户名称
    c_provider TEXT NOT NULL, -- 云厂商（tencent/aliyun/aws）
    c_secret_id TEXT NOT NULL, -- 密钥ID
    c_secret_key TEXT NOT NULL, -- 密钥（加密存储）
    c_extra_config TEXT NOT NULL DEFAULT '{}', -- 额外配置（JSON格式，如region等）
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    UNIQUE (c_account_id, c_invalid)
);

-- ************ 创建云函数数据采集器节点信息表 (增强版) ************
DROP TABLE IF EXISTS t_cloud_nodes;
CREATE TABLE IF NOT EXISTS t_cloud_nodes (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_node_id TEXT NOT NULL, -- 节点唯一标识（如：scf-collector-01）
    c_cloud_account_id TEXT NOT NULL DEFAULT '', -- 云账户ID
    c_namespace TEXT NOT NULL DEFAULT '', -- 命名空间
    c_node_type TEXT NOT NULL DEFAULT 'scf', -- 节点类型（scf=云函数，server=服务器）
    c_region TEXT NOT NULL DEFAULT '', -- 部署地区（如：ap-guangzhou）
    c_ip_address TEXT NOT NULL DEFAULT '', -- IP地址
    c_version TEXT NOT NULL DEFAULT '', -- 采集器版本
    c_supported_collectors TEXT NOT NULL DEFAULT '[]', -- 支持的采集器类型（JSON数组）
    c_capacity TEXT NOT NULL DEFAULT '{}', -- 节点能力（JSON：{cpu:2,memory:512,max_tasks:10}）
    c_current_load TEXT NOT NULL DEFAULT '{}', -- 当前负载（JSON：{cpu_usage:20,memory_usage:30,running_tasks:3}）
    c_status INTEGER NOT NULL DEFAULT 0, -- 节点状态（0=离线，1=在线，2=维护中，3=过载）
    c_last_heartbeat DATETIME, -- 最后心跳时间
    c_enabled TEXT NOT NULL DEFAULT 'true', -- 是否启用（字符串类型）
    c_metadata TEXT NOT NULL DEFAULT '{}', -- 节点额外信息（JSON格式）
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    UNIQUE (c_node_id)
);

-- ************ 创建采集任务配置表（替代原t_node_collectors_conf） ************
CREATE TABLE IF NOT EXISTS t_collector_task_config (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_task_id TEXT NOT NULL, -- 任务唯一标识
    c_project_id TEXT NOT NULL, -- 项目ID（关联到项目表）
    c_dataset_id TEXT NOT NULL, -- 数据集ID（关联到数据集表）
    c_task_type TEXT NOT NULL, -- 任务类型（object_list=对象列表采集，data_collect=数据采集）
    c_collector_type TEXT NOT NULL, -- 采集器类型（kline/ticker/orderbook/trade/news等）
    c_source_name TEXT NOT NULL DEFAULT '', -- 数据源名称（binance/okx等）
    
    -- 任务分配配置
    c_assignment_type TEXT NOT NULL DEFAULT 'auto', -- 分配类型（auto=自动分配，fixed=固定节点，pattern=通配符匹配）
    c_assigned_nodes TEXT NOT NULL DEFAULT '[]', -- 指定节点列表（JSON数组，fixed类型时使用）
    c_node_pattern TEXT NOT NULL DEFAULT '', -- 节点匹配模式（pattern类型时使用，如：scf-collector-*）
    c_load_balance_strategy TEXT NOT NULL DEFAULT 'round_robin', -- 负载均衡策略（round_robin/least_load/random）
    
    -- 采集目标配置
    c_target_objects TEXT NOT NULL DEFAULT '[]', -- 目标对象列表（JSON数组，如交易对列表）
    c_object_pattern TEXT NOT NULL DEFAULT '', -- 对象匹配模式（支持通配符，如：*USDT）
    c_force_objects TEXT NOT NULL DEFAULT '{}', -- 强制指定对象（JSON：{node_id:[objects]}）
    
    -- 采集参数配置
    c_collect_params TEXT NOT NULL DEFAULT '{}', -- 采集参数（JSON：{intervals:["1m","5m"],depth:20}）
    c_schedule_config TEXT NOT NULL DEFAULT '{}', -- 调度配置（JSON：{cron:"*/5 * * * *",retry:3,timeout:300}）
    
    -- 任务状态
    c_enabled TEXT NOT NULL DEFAULT 'true', -- 是否启用（"true"=启用，"false"=禁用）
    c_priority INTEGER NOT NULL DEFAULT 0, -- 优先级（数值越大优先级越高）
    c_last_dispatch_time DATETIME, -- 最后分发时间
    c_last_dispatch_result TEXT NOT NULL DEFAULT '', -- 最后分发结果
    
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    
    UNIQUE (c_task_id)
);

-- ************ 创建采集任务实例表（记录实际分配的任务） ************
CREATE TABLE IF NOT EXISTS t_collector_task_instances (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_instance_id TEXT NOT NULL, -- 实例唯一标识
    c_task_id TEXT NOT NULL, -- 任务ID（关联配置表）
    c_project_id TEXT NOT NULL, -- 项目ID（关联到项目表）
    c_dataset_id TEXT NOT NULL, -- 数据集ID（关联到数据集表）
    c_node_id TEXT NOT NULL, -- 执行节点ID
    c_target_objects TEXT NOT NULL DEFAULT '[]', -- 分配的对象列表（JSON数组）
    c_execution_params TEXT NOT NULL DEFAULT '{}', -- 执行参数（合并后的最终参数）
    c_status INTEGER NOT NULL DEFAULT 0, -- 状态（0=待执行，1=执行中，2=成功，3=失败，4=超时，5=已取消）
    c_start_time DATETIME, -- 开始时间
    c_end_time DATETIME, -- 结束时间
    c_result TEXT NOT NULL DEFAULT '{}', -- 执行结果（JSON格式）
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    
    UNIQUE (c_instance_id)
);

-- ************ 创建云函数采集器相关索引 ************
-- 节点表索引
CREATE INDEX IF NOT EXISTS idx_nodes_status ON t_cloud_nodes(c_status);
CREATE INDEX IF NOT EXISTS idx_nodes_type ON t_cloud_nodes(c_node_type);
CREATE INDEX IF NOT EXISTS idx_nodes_heartbeat ON t_cloud_nodes(c_last_heartbeat);
CREATE INDEX IF NOT EXISTS idx_nodes_enabled ON t_cloud_nodes(c_enabled);

-- 任务配置表索引
CREATE INDEX IF NOT EXISTS idx_task_config_project_dataset ON t_collector_task_config(c_project_id, c_dataset_id);
CREATE INDEX IF NOT EXISTS idx_task_config_task_type ON t_collector_task_config(c_task_type);
CREATE INDEX IF NOT EXISTS idx_task_config_collector_type ON t_collector_task_config(c_collector_type);
CREATE INDEX IF NOT EXISTS idx_task_config_source ON t_collector_task_config(c_source_name);
CREATE INDEX IF NOT EXISTS idx_task_config_assignment ON t_collector_task_config(c_assignment_type);
CREATE INDEX IF NOT EXISTS idx_task_config_enabled_priority ON t_collector_task_config(c_enabled, c_priority);

-- 任务实例表索引
CREATE INDEX IF NOT EXISTS idx_task_instances_task_node ON t_collector_task_instances(c_task_id, c_node_id);
CREATE INDEX IF NOT EXISTS idx_task_instances_status_time ON t_collector_task_instances(c_status, c_ctime);
CREATE INDEX IF NOT EXISTS idx_task_instances_node_status ON t_collector_task_instances(c_node_id, c_status);
CREATE INDEX IF NOT EXISTS idx_task_instances_create_time ON t_collector_task_instances(c_ctime DESC);
CREATE INDEX IF NOT EXISTS idx_task_instances_project_dataset ON t_collector_task_instances(c_project_id, c_dataset_id);

-- ************ 创建云账户相关索引 ************
CREATE INDEX IF NOT EXISTS idx_cloud_accounts_provider ON t_cloud_accounts(c_provider);
CREATE INDEX IF NOT EXISTS idx_cloud_accounts_invalid ON t_cloud_accounts(c_invalid);

-- ************ 创建云函数采集器相关触发器 ************
-- 云账户表更新触发器
DROP TRIGGER IF EXISTS update_cloud_accounts_mtime;
CREATE TRIGGER update_cloud_accounts_mtime AFTER UPDATE ON t_cloud_accounts BEGIN UPDATE t_cloud_accounts SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 节点表更新触发器
DROP TRIGGER IF EXISTS update_scf_collector_nodes_mtime;
CREATE TRIGGER update_scf_collector_nodes_mtime AFTER UPDATE ON t_cloud_nodes BEGIN UPDATE t_cloud_nodes SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 任务配置表更新触发器
DROP TRIGGER IF EXISTS update_task_config_mtime;
CREATE TRIGGER update_task_config_mtime AFTER UPDATE ON t_collector_task_config BEGIN UPDATE t_collector_task_config SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 任务实例表更新触发器
DROP TRIGGER IF EXISTS update_task_instances_mtime;
CREATE TRIGGER update_task_instances_mtime AFTER UPDATE ON t_collector_task_instances BEGIN UPDATE t_collector_task_instances SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ 异步任务管理表设计 ============

-- ************ 异步任务表 ************
CREATE TABLE t_async_tasks (
    c_task_id TEXT NOT NULL,                                   -- 任务UUID
    c_task_type TEXT NOT NULL,                                 -- 任务类型: BATCH_CREATE_NODE, BATCH_UPDATE_NODE, BATCH_DELETE_NODE
    c_task_status INTEGER NOT NULL DEFAULT 1,                  -- 任务状态: 1-处理中, 2-成功, 3-失败, 4-部分成功
    c_total_count INTEGER DEFAULT 0,                           -- 总任务数
    c_success_count INTEGER DEFAULT 0,                         -- 成功数
    c_failed_count INTEGER DEFAULT 0,                           -- 失败数
    c_request_params TEXT,                                      -- 请求参数 (JSON格式)
    c_result_data TEXT,                                         -- 执行结果 (JSON格式)
    c_error_message TEXT,                                       -- 错误信息
    c_started_time DATETIME,                                    -- 开始执行时间
    c_completed_time DATETIME,                                  -- 完成时间
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 修改时间
    
    UNIQUE (c_task_id)
);

-- ************ 异步任务详情表 (记录批量操作中每个子任务) ************
CREATE TABLE t_async_task_details (
    c_task_id TEXT NOT NULL,                                   -- 任务UUID (关联主任务)
    c_item_id TEXT NOT NULL,                                   -- 操作项ID (如node_id)
    c_item_name TEXT DEFAULT '',                               -- 操作项名称
    c_status INTEGER NOT NULL DEFAULT 1,                       -- 状态: 1-待处理, 2-处理中, 3-成功, 4-失败
    c_error_message TEXT,                                       -- 错误信息
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 修改时间
    
    PRIMARY KEY (c_task_id, c_item_id)                        -- 联合主键
);

-- ************ 创建异步任务相关索引 ************
CREATE INDEX idx_async_tasks_status ON t_async_tasks(c_task_status);
CREATE INDEX idx_async_tasks_ctime ON t_async_tasks(c_ctime);
CREATE INDEX idx_task_details_task_id ON t_async_task_details(c_task_id);
CREATE INDEX idx_task_details_status ON t_async_task_details(c_status);

-- ************ 创建异步任务相关触发器 ************
-- 异步任务表更新触发器
DROP TRIGGER IF EXISTS update_async_tasks_mtime;
CREATE TRIGGER update_async_tasks_mtime AFTER UPDATE ON t_async_tasks BEGIN UPDATE t_async_tasks SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 异步任务详情表更新触发器
DROP TRIGGER IF EXISTS update_async_task_details_mtime;
CREATE TRIGGER update_async_task_details_mtime AFTER UPDATE ON t_async_task_details BEGIN UPDATE t_async_task_details SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

