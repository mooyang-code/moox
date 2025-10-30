
-- ============ MooX 认证系统数据库表设计 ============

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
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                -- 修改时间
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
CREATE UNIQUE INDEX idx_users_user_id ON t_users(c_user_id);
CREATE UNIQUE INDEX idx_users_username ON t_users(c_username);
CREATE INDEX idx_users_email ON t_users(c_email);
CREATE INDEX idx_users_status ON t_users(c_status);
CREATE INDEX idx_users_role ON t_users(c_role);
CREATE INDEX idx_users_last_login ON t_users(c_last_login_at);

-- 令牌表索引
CREATE UNIQUE INDEX idx_tokens_jti ON t_active_tokens(c_jti);
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
CREATE TRIGGER update_users_mtime AFTER UPDATE ON t_users BEGIN 
    UPDATE t_users SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 活跃令牌表触发器 - 更新时间
CREATE TRIGGER update_tokens_mtime AFTER UPDATE ON t_active_tokens BEGIN 
    UPDATE t_active_tokens SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;


-- ============ 云函数数据采集器系统表设计 ============

-- ************ 云账户配置表 ************
CREATE TABLE IF NOT EXISTS t_cloud_accounts (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_account_id TEXT NOT NULL, -- 账户唯一标识
    c_account_name TEXT NOT NULL, -- 账户名称
    c_provider TEXT NOT NULL, -- 云厂商（tencent/aliyun/aws）
    c_secret_id TEXT NOT NULL, -- 密钥ID
    c_secret_key TEXT NOT NULL, -- 密钥（加密存储）
    c_app_id TEXT NOT NULL DEFAULT '', -- 应用ID
    c_cos_region TEXT NOT NULL DEFAULT '', -- COS区域
    c_cos_bucket TEXT NOT NULL DEFAULT '', -- COS桶名
    c_extra_config TEXT NOT NULL DEFAULT '{}', -- 额外配置（JSON格式，如region等）
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP -- 修改时间
);

-- ************ 创建云函数数据采集器节点信息表 ************
CREATE TABLE IF NOT EXISTS t_cloud_nodes (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_node_id TEXT NOT NULL, -- 节点唯一标识（如：scf-collector-01）
    c_cloud_account_id TEXT NOT NULL DEFAULT '', -- 云账户ID
    c_package_id TEXT DEFAULT '', -- 代码包ID，记录该节点当前部署的代码包(11位随机字符串)
    c_namespace TEXT NOT NULL DEFAULT '', -- 命名空间
    c_node_type TEXT NOT NULL DEFAULT 'scf', -- 节点类型（scf=云函数，server=服务器）
    c_region TEXT NOT NULL DEFAULT '', -- 部署地区（如：ap-guangzhou）
    c_ip_address TEXT NOT NULL DEFAULT '', -- IP地址
    c_supported_collectors TEXT NOT NULL DEFAULT '[]', -- 支持的采集器类型（JSON数组）
    c_metadata TEXT NOT NULL DEFAULT '{}', -- 节点额外信息（JSON格式）
    c_timeout_threshold INTEGER DEFAULT 35, -- 超时阈值（秒），0表示使用全局默认值
    c_heartbeat_interval INTEGER DEFAULT 10, -- 心跳间隔（秒），0表示使用全局默认值
    c_probe_enabled BOOLEAN DEFAULT true, -- 是否启用探测
    c_probe_url TEXT DEFAULT '', -- 探测URL
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    
    FOREIGN KEY (c_package_id) REFERENCES t_function_packages(c_package_id)
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
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP -- 修改时间
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

    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP -- 修改时间
);

-- ************ 创建云函数采集器相关索引 ************
-- 节点表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_node_id ON t_cloud_nodes(c_node_id);
CREATE INDEX IF NOT EXISTS idx_nodes_type ON t_cloud_nodes(c_node_type);

-- 任务配置表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_config_task_id ON t_collector_task_config(c_task_id);
CREATE INDEX IF NOT EXISTS idx_task_config_project_dataset ON t_collector_task_config(c_project_id, c_dataset_id);
CREATE INDEX IF NOT EXISTS idx_task_config_task_type ON t_collector_task_config(c_task_type);
CREATE INDEX IF NOT EXISTS idx_task_config_collector_type ON t_collector_task_config(c_collector_type);
CREATE INDEX IF NOT EXISTS idx_task_config_source ON t_collector_task_config(c_source_name);
CREATE INDEX IF NOT EXISTS idx_task_config_assignment ON t_collector_task_config(c_assignment_type);
CREATE INDEX IF NOT EXISTS idx_task_config_enabled_priority ON t_collector_task_config(c_enabled, c_priority);

-- 任务实例表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_instances_instance_id ON t_collector_task_instances(c_instance_id);
CREATE INDEX IF NOT EXISTS idx_task_instances_task_node ON t_collector_task_instances(c_task_id, c_node_id);
CREATE INDEX IF NOT EXISTS idx_task_instances_status_time ON t_collector_task_instances(c_status, c_ctime);
CREATE INDEX IF NOT EXISTS idx_task_instances_node_status ON t_collector_task_instances(c_node_id, c_status);
CREATE INDEX IF NOT EXISTS idx_task_instances_create_time ON t_collector_task_instances(c_ctime DESC);
CREATE INDEX IF NOT EXISTS idx_task_instances_project_dataset ON t_collector_task_instances(c_project_id, c_dataset_id);

-- ************ 创建云账户相关索引 ************
CREATE UNIQUE INDEX IF NOT EXISTS idx_cloud_accounts_account_id_invalid ON t_cloud_accounts(c_account_id, c_invalid);
CREATE INDEX IF NOT EXISTS idx_cloud_accounts_provider ON t_cloud_accounts(c_provider);
CREATE INDEX IF NOT EXISTS idx_cloud_accounts_invalid ON t_cloud_accounts(c_invalid);

-- ************ 创建云函数采集器相关触发器 ************
-- 云账户表更新触发器
DROP TRIGGER IF EXISTS update_cloud_accounts_mtime;
CREATE TRIGGER update_cloud_accounts_mtime AFTER UPDATE ON t_cloud_accounts BEGIN 
    UPDATE t_cloud_accounts SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 节点表更新触发器
DROP TRIGGER IF EXISTS update_scf_collector_nodes_mtime;
CREATE TRIGGER update_scf_collector_nodes_mtime AFTER UPDATE ON t_cloud_nodes BEGIN 
    UPDATE t_cloud_nodes SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 任务配置表更新触发器
DROP TRIGGER IF EXISTS update_task_config_mtime;
CREATE TRIGGER update_task_config_mtime AFTER UPDATE ON t_collector_task_config BEGIN 
    UPDATE t_collector_task_config SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 任务实例表更新触发器
DROP TRIGGER IF EXISTS update_task_instances_mtime;
CREATE TRIGGER update_task_instances_mtime AFTER UPDATE ON t_collector_task_instances BEGIN 
    UPDATE t_collector_task_instances SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ 异步任务管理表设计 (Job-Task 模型) ============

-- ************ 异步任务Job表 ************
CREATE TABLE t_async_jobs (
    c_job_id TEXT NOT NULL,                                    -- 用户一次提交的批次ID
    c_request_params TEXT,                                      -- 请求参数 (JSON格式)

    c_total_task_cnt INTEGER DEFAULT 0,                         -- 总任务数
    c_success_task_cnt INTEGER DEFAULT 0,                       -- 成功数
    c_failed_task_cnt INTEGER DEFAULT 0,                        -- 失败数
    c_is_started INTEGER DEFAULT 0,                             -- 任务是否启动

    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

-- ************ 异步任务Task表 ************
CREATE TABLE t_async_job_tasks (
    c_task_id TEXT NOT NULL,                                   -- 任务UUID
    c_job_id TEXT NOT NULL,                                    -- 任务所属的用户一次提交的批次ID

    c_task_type TEXT NOT NULL,                                 -- 任务类型: CREATE_NODE, UPDATE_NODE, DELETE_NODE
    c_task_status INTEGER NOT NULL DEFAULT 1,                  -- 状态: 1-处理中, 2-成功, 3-失败, 4-部分成功

    c_request_params TEXT,                                      -- 请求参数 (JSON格式)
    c_result_data TEXT,                                         -- 执行结果 (JSON格式)
    c_error_message TEXT,                                       -- 错误信息
    c_started_time DATETIME,                                    -- 开始执行时间
    c_completed_time DATETIME,                                  -- 完成时间
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 修改时间
);

-- ************ 创建异步任务相关索引 ************
CREATE UNIQUE INDEX idx_async_jobs_job_id ON t_async_jobs(c_job_id);
CREATE INDEX idx_async_jobs_ctime ON t_async_jobs(c_ctime);
CREATE INDEX idx_async_jobs_is_started ON t_async_jobs(c_is_started);
CREATE UNIQUE INDEX idx_async_job_tasks_task_id ON t_async_job_tasks(c_task_id);
CREATE INDEX idx_async_job_tasks_job_id ON t_async_job_tasks(c_job_id);
CREATE INDEX idx_async_job_tasks_status ON t_async_job_tasks(c_task_status);
CREATE INDEX idx_async_job_tasks_ctime ON t_async_job_tasks(c_ctime);

-- ************ 创建异步任务相关触发器 ************
-- 异步任务Job表更新触发器
DROP TRIGGER IF EXISTS update_async_jobs_mtime;
CREATE TRIGGER update_async_jobs_mtime AFTER UPDATE ON t_async_jobs BEGIN 
    UPDATE t_async_jobs SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 异步任务Task表更新触发器
DROP TRIGGER IF EXISTS update_async_job_tasks_mtime;
CREATE TRIGGER update_async_job_tasks_mtime AFTER UPDATE ON t_async_job_tasks BEGIN 
    UPDATE t_async_job_tasks SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ************ 节点心跳表 ************
CREATE TABLE t_node_heartbeat (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 主键ID
    c_node_id TEXT NOT NULL,                                   -- 节点ID
    c_last_heartbeat DATETIME NOT NULL,                        -- 最后心跳时间
    c_task_version INTEGER DEFAULT 0,                          -- 任务版本号
    c_task_hash TEXT DEFAULT '',                               -- 任务哈希值
    c_status INTEGER DEFAULT 1,                                -- 节点状态: 1=正常,0=离线
    c_metrics TEXT,                                             -- 节点指标信息（JSON）
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 更新时间
);

-- 节点心跳表索引
CREATE UNIQUE INDEX idx_node_heartbeat_node_id ON t_node_heartbeat(c_node_id);
CREATE INDEX idx_node_heartbeat_time ON t_node_heartbeat(c_last_heartbeat);
CREATE INDEX idx_node_heartbeat_status ON t_node_heartbeat(c_status);

-- ************ 节点任务快照表 ************
CREATE TABLE t_node_task_snapshot (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 主键ID
    c_node_id TEXT NOT NULL,                                   -- 节点ID
    c_task_id TEXT NOT NULL,                                   -- 任务ID
    c_task_status TEXT DEFAULT '',                             -- 任务状态
    c_task_updated_at DATETIME,                                -- 任务更新时间
    c_sync_time DATETIME NOT NULL,                             -- 同步时间
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 更新时间
);

-- 节点任务快照表索引
CREATE INDEX idx_node_task_snapshot_node_task ON t_node_task_snapshot(c_node_id, c_task_id);
CREATE INDEX idx_node_task_snapshot_sync_time ON t_node_task_snapshot(c_sync_time);

-- 节点心跳表更新触发器
DROP TRIGGER IF EXISTS update_node_heartbeat_mtime;
CREATE TRIGGER update_node_heartbeat_mtime AFTER UPDATE ON t_node_heartbeat BEGIN 
    UPDATE t_node_heartbeat SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 节点任务快照表更新触发器
DROP TRIGGER IF EXISTS update_node_task_snapshot_mtime;
CREATE TRIGGER update_node_task_snapshot_mtime AFTER UPDATE ON t_node_task_snapshot BEGIN 
    UPDATE t_node_task_snapshot SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ 心跳服务表设计 ============

-- ************ 心跳节点主表 ************
CREATE TABLE IF NOT EXISTS t_heartbeat_nodes (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 记录ID
    c_node_id TEXT NOT NULL,                                   -- 节点ID
    c_node_type TEXT NOT NULL,                                 -- 节点类型
    c_source_service TEXT DEFAULT '',                          -- 来源服务

    -- 时间信息
    c_last_heartbeat DATETIME,                                 -- 最后心跳时间
    c_first_heartbeat DATETIME,                                -- 首次心跳时间
    
    -- 统计数据
    c_consecutive_timeouts INTEGER DEFAULT 0,                  -- 连续超时次数
    c_total_timeouts INTEGER DEFAULT 0,                        -- 累计超时次数
    c_total_heartbeats INTEGER DEFAULT 0,                      -- 累计心跳次数
    
    -- 扩展数据
    c_metadata TEXT DEFAULT '{}',                              -- 元数据（JSON格式）
    
    -- 探测配置
    c_last_probe_time DATETIME,                                -- 最后探测时间
    c_last_probe_result TEXT DEFAULT '',                       -- 最后探测结果
    
    -- 审计字段
    c_invalid INTEGER NOT NULL DEFAULT 0,                      -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                 -- 更新时间
);

-- ************ 创建心跳服务相关索引 ************
-- 心跳节点表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_heartbeat_nodes_node_id ON t_heartbeat_nodes(c_node_id);
CREATE INDEX IF NOT EXISTS idx_heartbeat_nodes_node_type ON t_heartbeat_nodes(c_node_type);
CREATE INDEX IF NOT EXISTS idx_heartbeat_nodes_last_heartbeat ON t_heartbeat_nodes(c_last_heartbeat);
CREATE INDEX IF NOT EXISTS idx_heartbeat_nodes_source_service ON t_heartbeat_nodes(c_source_service);

-- ************ 创建心跳服务相关触发器 ************
-- 心跳节点表更新触发器
DROP TRIGGER IF EXISTS update_heartbeat_nodes_mtime;
CREATE TRIGGER update_heartbeat_nodes_mtime AFTER UPDATE ON t_heartbeat_nodes BEGIN 
    UPDATE t_heartbeat_nodes SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ 云函数代码包管理系统表设计 ============

-- ************ 云函数代码包表 ************
CREATE TABLE IF NOT EXISTS t_function_packages (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,               -- 主键ID
    c_package_id TEXT NOT NULL,                                    -- 代码包唯一标识(11位随机字符串)
    c_package_name TEXT NOT NULL,                                  -- 代码包名称
    c_version TEXT NOT NULL,                                       -- 版本号
    c_description TEXT DEFAULT '',                                 -- 版本描述
    c_runtime TEXT NOT NULL,                                       -- 运行时环境(Go1, Python3.7等)
    c_package_type TEXT NOT NULL DEFAULT 'data_collector',         -- 函数包类型: data_collector=数据采集类型, factor_calculator=因子计算类型

    -- 文件信息
    c_original_filename TEXT NOT NULL,                             -- 原始文件名
    c_file_size INTEGER NOT NULL,                                  -- 文件大小(字节)
    c_file_md5 TEXT NOT NULL,                                      -- 文件MD5校验

    -- COS存储信息
    c_cloud_account_id TEXT DEFAULT '',                            -- 云账户ID，记录使用哪个账户上传的COS
    c_cos_region TEXT DEFAULT '',                                  -- COS地区，记录代码包上传到COS的哪个地区
    c_cos_bucket TEXT NOT NULL,                                    -- COS桶名
    c_cos_path TEXT NOT NULL,                                      -- COS文件路径
    c_cos_url TEXT DEFAULT '',                                     -- COS访问URL

    -- 状态管理
    c_status INTEGER NOT NULL DEFAULT 0,                           -- 状态: 0=上传中, 1=可用, 2=已删除, 3=上传失败
    c_upload_progress INTEGER DEFAULT 0,                           -- 上传进度(0-100)
    c_error_message TEXT DEFAULT '',                               -- 错误信息

    -- 使用统计
    c_last_deploy_time DATETIME,                                   -- 最后部署时间

    -- 审计字段
    c_invalid INTEGER NOT NULL DEFAULT 0,                          -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                    -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                     -- 修改时间
);

-- ************ 云函数部署记录表 ************
CREATE TABLE IF NOT EXISTS t_function_deployments (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,               -- 主键ID
    c_package_id TEXT NOT NULL,                                    -- 代码包ID(11位随机字符串)
    c_cloud_account_id TEXT NOT NULL,                              -- 云账户ID
    c_function_name TEXT NOT NULL,                                 -- 函数名
    c_namespace TEXT DEFAULT 'default',                            -- 命名空间

    -- 部署配置
    c_environment TEXT DEFAULT '{}',                               -- 环境变量(JSON格式)

    -- 部署状态
    c_deploy_status INTEGER DEFAULT 0,                             -- 部署状态: 0=进行中, 1=成功, 2=失败
    c_deploy_message TEXT DEFAULT '',                              -- 部署结果信息

    c_invalid INTEGER NOT NULL DEFAULT 0,                          -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                    -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,                    -- 修改时间

    FOREIGN KEY (c_package_id) REFERENCES t_function_packages(c_package_id),
    FOREIGN KEY (c_cloud_account_id) REFERENCES t_cloud_accounts(c_account_id)
);

-- ************ 创建云函数代码包相关索引 ************
-- 代码包表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_function_packages_package_id ON t_function_packages(c_package_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_function_packages_name_version_invalid ON t_function_packages(c_package_name, c_version, c_invalid);
CREATE INDEX IF NOT EXISTS idx_function_packages_status ON t_function_packages(c_status);
CREATE INDEX IF NOT EXISTS idx_function_packages_runtime ON t_function_packages(c_runtime);
CREATE INDEX IF NOT EXISTS idx_function_packages_package_type ON t_function_packages(c_package_type);
CREATE INDEX IF NOT EXISTS idx_function_packages_ctime ON t_function_packages(c_ctime);
CREATE INDEX IF NOT EXISTS idx_function_packages_invalid ON t_function_packages(c_invalid);

-- 部署记录表索引
CREATE INDEX IF NOT EXISTS idx_function_deployments_package_id ON t_function_deployments(c_package_id);
CREATE INDEX IF NOT EXISTS idx_function_deployments_account_id ON t_function_deployments(c_cloud_account_id);
CREATE INDEX IF NOT EXISTS idx_function_deployments_deploy_status ON t_function_deployments(c_deploy_status);
CREATE INDEX IF NOT EXISTS idx_function_deployments_function_name ON t_function_deployments(c_function_name);
CREATE INDEX IF NOT EXISTS idx_function_deployments_ctime ON t_function_deployments(c_ctime);
CREATE INDEX IF NOT EXISTS idx_function_deployments_invalid ON t_function_deployments(c_invalid);

-- ************ 创建云函数代码包相关触发器 ************
-- 代码包表更新触发器
DROP TRIGGER IF EXISTS update_function_packages_mtime;
CREATE TRIGGER update_function_packages_mtime AFTER UPDATE ON t_function_packages BEGIN 
    UPDATE t_function_packages SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 部署记录表更新触发器
DROP TRIGGER IF EXISTS update_function_deployments_mtime;
CREATE TRIGGER update_function_deployments_mtime AFTER UPDATE ON t_function_deployments BEGIN 
    UPDATE t_function_deployments SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

