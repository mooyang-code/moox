
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
    c_tag TEXT NOT NULL DEFAULT '', -- 标签（国内/海外）
    c_ip_address TEXT NOT NULL DEFAULT '', -- IP地址
    c_supported_collectors TEXT NOT NULL DEFAULT '[]', -- 支持的采集器类型（JSON数组:["kline"]）
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


-- ************ 创建云函数采集器相关索引 ************
-- 节点表索引
CREATE INDEX IF NOT EXISTS idx_node_id_invalid ON t_cloud_nodes(c_node_id, c_invalid);
CREATE INDEX IF NOT EXISTS idx_nodes_type ON t_cloud_nodes(c_node_type);

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

-- 节点任务快照表更新触发器
DROP TRIGGER IF EXISTS update_node_task_snapshot_mtime;
CREATE TRIGGER update_node_task_snapshot_mtime AFTER UPDATE ON t_node_task_snapshot BEGIN 
    UPDATE t_node_task_snapshot SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ 采集器任务规则系统表设计 ============

-- ************ 采集任务规则表 ************
CREATE TABLE IF NOT EXISTS t_collector_task_rules (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_rule_id TEXT NOT NULL, -- 规则唯一标识
    c_data_type TEXT NOT NULL, -- 数据类型（kline/ticker/orderbook/trade/news/list等）
    c_data_source TEXT NOT NULL DEFAULT '', -- 数据源名称（binance/okx等）
    c_collect_params TEXT NOT NULL DEFAULT '{}', -- 采集参数（JSON：{intervals:["1m","5m"],depth:20, objects:["BTC-USDT","ETH-USDT"]}）
    
    -- 任务分配配置
    c_assignment_type TEXT NOT NULL DEFAULT 'auto', -- 分配类型（auto=自动分配，fixed=固定节点，pattern=通配符匹配，tag=标签匹配）
    c_assigned_nodes TEXT NOT NULL DEFAULT '[]', -- 指定节点列表（JSON数组，fixed类型时使用）
    c_node_pattern TEXT NOT NULL DEFAULT '', -- 节点匹配模式（pattern类型时使用，如：scf-collector-*）
    c_node_tags TEXT NOT NULL DEFAULT '[]', -- 节点标签列表（JSON数组，tag类型时使用，如：["国内","海外"]）
    
    -- 任务状态
    c_enabled TEXT NOT NULL DEFAULT 'true', -- 是否启用（"true"=启用，"false"=禁用）
    c_creator TEXT NOT NULL DEFAULT '', -- 创建人
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP -- 修改时间
);

-- ************ 采集任务实例表 ************
CREATE TABLE IF NOT EXISTS t_collector_task_instances (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_task_id TEXT NOT NULL, -- 任务唯一标识
    c_rule_id TEXT NOT NULL, -- 规则ID（关联规则表）

    -- 计划执行节点（定时重算时写入）
    c_planned_exec_node TEXT NOT NULL DEFAULT '', -- 计划执行节点ID
    c_last_exec_node TEXT NOT NULL DEFAULT '', -- 最后执行节点ID（客户端上报时写入）
    c_last_exec_status INTEGER NOT NULL DEFAULT 0, -- 最后执行状态（0=待执行，1=执行中，2=成功，3=部分失败，4=失败）

    c_symbol TEXT NOT NULL DEFAULT '', -- 标的符号（交易对，如：BTC-USDT，空字符串表示不按标的拆分）
    c_collect_data_type TEXT NOT NULL DEFAULT '', -- 采集数据类型（从 c_task_params 中的 data_type 提取，用于快速查询）
    c_interval TEXT NOT NULL DEFAULT 'default', -- 时间间隔（1m/5m/1h等，非interval类任务为"default"）
    c_task_params TEXT NOT NULL DEFAULT '{}', -- 任务执行参数（JSON格式:{"symbol":"BTCUSDT","intervals":["1m"],"limit":100}）

    c_last_exec_time DATETIME, -- 最后执行时间
    c_result TEXT NOT NULL DEFAULT '{}', -- 执行结果（JSON格式）

    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记（软删除：0=有效，1=已删除）
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP -- 修改时间
);

-- ************ 创建采集任务规则相关索引 ************
-- 任务规则表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_task_rules_rule_id ON t_collector_task_rules(c_rule_id);
CREATE INDEX IF NOT EXISTS idx_collector_task_rules_data_type ON t_collector_task_rules(c_data_type);
CREATE INDEX IF NOT EXISTS idx_collector_task_rules_data_source ON t_collector_task_rules(c_data_source);
CREATE INDEX IF NOT EXISTS idx_collector_task_rules_assignment_type ON t_collector_task_rules(c_assignment_type);
CREATE INDEX IF NOT EXISTS idx_collector_task_rules_enabled ON t_collector_task_rules(c_enabled);

-- 任务实例表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_task_instances_task_id ON t_collector_task_instances(c_task_id);
CREATE INDEX IF NOT EXISTS idx_collector_task_instances_rule_id ON t_collector_task_instances(c_rule_id);
CREATE INDEX IF NOT EXISTS idx_collector_task_instances_rule_planned_node_symbol ON t_collector_task_instances(c_rule_id, c_planned_exec_node, c_symbol);
CREATE INDEX IF NOT EXISTS idx_collector_task_instances_planned_node ON t_collector_task_instances(c_planned_exec_node);
CREATE INDEX IF NOT EXISTS idx_collector_task_instances_planned_node_status ON t_collector_task_instances(c_planned_exec_node, c_last_exec_status);
CREATE INDEX IF NOT EXISTS idx_collector_task_instances_planned_node_interval ON t_collector_task_instances(c_planned_exec_node, c_interval);
CREATE INDEX IF NOT EXISTS idx_collector_task_instances_invalid ON t_collector_task_instances(c_invalid); -- 用于过滤软删除记录
CREATE INDEX IF NOT EXISTS idx_collector_task_instances_create_time ON t_collector_task_instances(c_ctime DESC);

-- ************ 创建采集任务规则相关触发器 ************
-- 任务规则表更新触发器
DROP TRIGGER IF EXISTS update_collector_task_rules_mtime;
CREATE TRIGGER update_collector_task_rules_mtime AFTER UPDATE ON t_collector_task_rules BEGIN 
    UPDATE t_collector_task_rules SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 任务实例表更新触发器
DROP TRIGGER IF EXISTS update_collector_task_instances_mtime;
CREATE TRIGGER update_collector_task_instances_mtime AFTER UPDATE ON t_collector_task_instances BEGIN 
    UPDATE t_collector_task_instances SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ 心跳服务表设计 ============

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

-- ************ 创建云函数代码包相关索引 ************
-- 代码包表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_function_packages_package_id ON t_function_packages(c_package_id);
CREATE INDEX IF NOT EXISTS idx_function_packages_status ON t_function_packages(c_status);
CREATE INDEX IF NOT EXISTS idx_function_packages_runtime ON t_function_packages(c_runtime);
CREATE INDEX IF NOT EXISTS idx_function_packages_package_type ON t_function_packages(c_package_type);
CREATE INDEX IF NOT EXISTS idx_function_packages_ctime ON t_function_packages(c_ctime);
CREATE INDEX IF NOT EXISTS idx_function_packages_invalid ON t_function_packages(c_invalid);

-- ************ 创建云函数代码包相关触发器 ************
-- 代码包表更新触发器
DROP TRIGGER IF EXISTS update_function_packages_mtime;
CREATE TRIGGER update_function_packages_mtime AFTER UPDATE ON t_function_packages BEGIN 
    UPDATE t_function_packages SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ============ 采集器数据类型配置系统表设计 ============

-- ************ 采集器数据类型配置表 ************
CREATE TABLE IF NOT EXISTS t_collector_data_type_configs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 主键ID
    c_data_type TEXT NOT NULL,                                  -- 数据类型标识 (kline/ticker/orderbook/trade/news)
    c_type_name TEXT NOT NULL,                                  -- 数据类型显示名称 (K线数据/Ticker数据等)
    c_type_desc TEXT NOT NULL DEFAULT '',                       -- 数据类型描述
    c_data_source_options TEXT NOT NULL DEFAULT '{}',           -- 数据源选项 (JSON字符串，格式同c_field_options)
    c_sort_order INTEGER DEFAULT 0,                             -- 排序顺序
    c_version INTEGER NOT NULL DEFAULT 1,                       -- 配置版本号
    c_invalid INTEGER NOT NULL DEFAULT 0,                       -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                 -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                  -- 修改时间
);

-- ************ 采集器参数字段配置表 ************
CREATE TABLE IF NOT EXISTS t_collector_field_configs (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,           -- 主键ID
    c_data_type TEXT NOT NULL,                                  -- 关联的数据类型 (kline/ticker/orderbook等)
    c_field_key TEXT NOT NULL,                                  -- 字段标识 (intervals/objects/depth等)
    c_field_name TEXT NOT NULL,                                 -- 字段显示名称 (时间周期/交易对象等)
    c_field_type TEXT NOT NULL,                                 -- 字段类型 (text/number/select/multi-select/checkbox/array)
    c_is_required BOOLEAN DEFAULT false,                        -- 是否必填
    c_default_value TEXT NOT NULL DEFAULT '',                   -- 默认值 (JSON字符串)
    c_field_options TEXT NOT NULL DEFAULT '',                   -- 字段选项 (JSON字符串)
    c_data_source_options TEXT NOT NULL DEFAULT '',             -- 数据源选项 (JSON字符串，格式同c_field_options)
    c_sort_order INTEGER DEFAULT 0,                             -- 字段排序
    c_invalid INTEGER NOT NULL DEFAULT 0,                       -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,                 -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP                  -- 修改时间
);

-- ************ 采集器数据类型配置相关索引 ************
-- 数据类型配置表索引
CREATE UNIQUE INDEX IF NOT EXISTS idx_collector_data_type_configs_data_type ON t_collector_data_type_configs(c_data_type, c_invalid);
CREATE INDEX IF NOT EXISTS idx_collector_data_type_configs_type_name ON t_collector_data_type_configs(c_type_name);
CREATE INDEX IF NOT EXISTS idx_collector_data_type_configs_invalid ON t_collector_data_type_configs(c_invalid);

-- 参数字段配置表索引
CREATE INDEX IF NOT EXISTS idx_collector_field_configs_data_type ON t_collector_field_configs(c_data_type);
CREATE INDEX IF NOT EXISTS idx_collector_field_configs_field_key ON t_collector_field_configs(c_field_key);
CREATE INDEX IF NOT EXISTS idx_collector_field_configs_type_sort ON t_collector_field_configs(c_data_type, c_sort_order);
CREATE INDEX IF NOT EXISTS idx_collector_field_configs_invalid ON t_collector_field_configs(c_invalid);

-- ************ 采集器数据类型配置相关触发器 ************
-- 数据类型配置表更新触发器
DROP TRIGGER IF EXISTS update_collector_data_type_configs_mtime;
CREATE TRIGGER update_collector_data_type_configs_mtime AFTER UPDATE ON t_collector_data_type_configs BEGIN 
    UPDATE t_collector_data_type_configs SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- 参数字段配置表更新触发器  
DROP TRIGGER IF EXISTS update_collector_field_configs_mtime;
CREATE TRIGGER update_collector_field_configs_mtime AFTER UPDATE ON t_collector_field_configs BEGIN 
    UPDATE t_collector_field_configs SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;

-- ************ 采集器数据类型配置初始数据 ************
-- 插入数据类型配置
INSERT OR IGNORE INTO t_collector_data_type_configs (c_data_type, c_type_name, c_type_desc, c_data_source_options, c_sort_order) VALUES
('symbol', '标的数据', '交易所标的列表同步配置', '{"options": [{"value": "okx", "label": "OKX"}, {"value": "binance", "label": "币安 (Binance)"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}]}', 1),
('kline', 'K线数据', '股票、期货等金融产品的K线数据采集配置', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 2),
('ticker', '逐笔数据', '实时逐笔交易数据采集配置', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 3),
('orderbook', '订单簿数据', '买卖盘深度数据采集配置', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}]}', 4),
('trade', '交易数据', '交易汇总数据采集配置', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 5),
('news', '新闻数据', '新闻资讯数据采集配置', '{"options": [{"value": "cryptonews", "label": "CryptoNews"}, {"value": "coindesk", "label": "CoinDesk"}, {"value": "cointelegraph", "label": "Cointelegraph"}, {"value": "decrypt", "label": "Decrypt"}, {"value": "theblock", "label": "The Block"}, {"value": "messari", "label": "Messari"}, {"value": "glassnode", "label": "Glassnode"}, {"value": "intoblock", "label": "IntoTheBlock"}]}', 6);

-- ************ 采集器参数字段配置初始数据 ************
-- 标的数据字段配置
INSERT OR IGNORE INTO t_collector_field_configs (c_data_type, c_field_key, c_field_name, c_field_type, c_is_required, c_default_value, c_field_options, c_data_source_options, c_sort_order) VALUES
('symbol', 'inst_types', '产品类型', 'multi-select', 1, '["SPOT"]', '{"options": [{"value": "SPOT", "label": "现货"}, {"value": "SWAP", "label": "永续合约"}, {"value": "FUTURES", "label": "交割合约"}, {"value": "OPTION", "label": "期权"}]}', '{"options": [{"value": "okx", "label": "OKX"}, {"value": "binance", "label": "币安 (Binance)"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}]}', 1),
('symbol', 'intervals', '时间周期', 'multi-select', 1, '["1m","5m","1h"]', '{"options": ["1m","3m","5m","15m","30m","1h","2h","4h","6h","8h","12h","1d","3d","1w","1M"]}', '{"options": [{"value": "okx", "label": "OKX"}, {"value": "binance", "label": "币安 (Binance)"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}]}', 2);

-- K线数据字段配置
INSERT OR IGNORE INTO t_collector_field_configs (c_data_type, c_field_key, c_field_name, c_field_type, c_is_required, c_default_value, c_field_options, c_data_source_options, c_sort_order) VALUES
('kline', 'symbol', '交易标的', 'text', 1, 'BTCUSDT', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 1),
('kline', 'intervals', '时间周期', 'multi-select', 1, '["1m","5m","1h"]', '{"options": ["1m","3m","5m","15m","30m","1h","2h","4h","6h","8h","12h","1d","3d","1w","1M"]}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 2),
('kline', 'limit', '数量限制', 'number', 0, '100', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 3);

-- 逐笔数据字段配置
INSERT OR IGNORE INTO t_collector_field_configs (c_data_type, c_field_key, c_field_name, c_field_type, c_is_required, c_default_value, c_field_options, c_data_source_options, c_sort_order) VALUES
('ticker', 'symbol', '交易标的', 'text', 1, 'BTCUSDT', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 1),
('ticker', 'limit', '数量限制', 'number', 0, '100', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 2),
('ticker', 'filter_0_volume', '最小成交额', 'number', 0, '0', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 3);

-- 订单簿数据字段配置
INSERT OR IGNORE INTO t_collector_field_configs (c_data_type, c_field_key, c_field_name, c_field_type, c_is_required, c_default_value, c_field_options, c_data_source_options, c_sort_order) VALUES
('orderbook', 'symbol', '交易标的', 'text', 1, 'BTCUSDT', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}]}', 1),
('orderbook', 'limit', '档位数量', 'number', 0, '100', '{"min": 1, "max": 5000}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}]}', 2),
('orderbook', 'depth', '深度类型', 'select', 0, 'step0', '{"options": [{"value": "step0", "label": "无深度合并"}, {"value": "step1", "label": "轻微深度合并"}, {"value": "step2", "label": "标准深度合并"}]}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}]}', 3);

-- 交易数据字段配置
INSERT OR IGNORE INTO t_collector_field_configs (c_data_type, c_field_key, c_field_name, c_field_type, c_is_required, c_default_value, c_field_options, c_data_source_options, c_sort_order) VALUES
('trade', 'symbol', '交易标的', 'text', 1, 'BTCUSDT', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 1),
('trade', 'limit', '数量限制', 'number', 0, '100', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 2),
('trade', 'start_time', '开始时间', 'datetime', 0, '', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 3),
('trade', 'end_time', '结束时间', 'datetime', 0, '', '{}', '{"options": [{"value": "binance", "label": "币安 (Binance)"}, {"value": "okx", "label": "OKX"}, {"value": "huobi", "label": "火币 (Huobi)"}, {"value": "bybit", "label": "Bybit"}, {"value": "bitget", "label": "Bitget"}, {"value": "kucoin", "label": "KuCoin"}, {"value": "gate", "label": "Gate.io"}, {"value": "mexc", "label": "MEXC"}, {"value": "bitfinex", "label": "Bitfinex"}, {"value": "coinbase", "label": "Coinbase"}]}', 4);

-- 新闻数据字段配置
INSERT OR IGNORE INTO t_collector_field_configs (c_data_type, c_field_key, c_field_name, c_field_type, c_is_required, c_default_value, c_field_options, c_data_source_options, c_sort_order) VALUES
('news', 'category', '新闻分类', 'select', 0, 'general', '{"options": [{"value": "general", "label": "综合"}, {"value": "crypto", "label": "加密货币"}, {"value": "finance", "label": "金融"}, {"value": "technology", "label": "科技"}]}', '{"options": [{"value": "cryptonews", "label": "CryptoNews"}, {"value": "coindesk", "label": "CoinDesk"}, {"value": "cointelegraph", "label": "Cointelegraph"}, {"value": "decrypt", "label": "Decrypt"}, {"value": "theblock", "label": "The Block"}, {"value": "messari", "label": "Messari"}, {"value": "glassnode", "label": "Glassnode"}, {"value": "intoblock", "label": "IntoTheBlock"}]}', 1),
('news', 'language', '语言', 'select', 0, 'zh-CN', '{"options": [{"value": "zh-CN", "label": "中文"}, {"value": "en-US", "label": "英文"}, {"value": "ja-JP", "label": "日文"}]}', '{"options": [{"value": "cryptonews", "label": "CryptoNews"}, {"value": "coindesk", "label": "CoinDesk"}, {"value": "cointelegraph", "label": "Cointelegraph"}, {"value": "decrypt", "label": "Decrypt"}, {"value": "theblock", "label": "The Block"}, {"value": "messari", "label": "Messari"}, {"value": "glassnode", "label": "Glassnode"}, {"value": "intoblock", "label": "IntoTheBlock"}]}', 2),
('news', 'limit', '数量限制', 'number', 0, '50', '{}', '{"options": [{"value": "cryptonews", "label": "CryptoNews"}, {"value": "coindesk", "label": "CoinDesk"}, {"value": "cointelegraph", "label": "Cointelegraph"}, {"value": "decrypt", "label": "Decrypt"}, {"value": "theblock", "label": "The Block"}, {"value": "messari", "label": "Messari"}, {"value": "glassnode", "label": "Glassnode"}, {"value": "intoblock", "label": "IntoTheBlock"}]}', 3),
('news', 'keywords', '关键词', 'text', 0, '', '{}', '{"options": [{"value": "cryptonews", "label": "CryptoNews"}, {"value": "coindesk", "label": "CoinDesk"}, {"value": "cointelegraph", "label": "Cointelegraph"}, {"value": "decrypt", "label": "Decrypt"}, {"value": "theblock", "label": "The Block"}, {"value": "messari", "label": "Messari"}, {"value": "glassnode", "label": "Glassnode"}, {"value": "intoblock", "label": "IntoTheBlock"}]}', 4);

-- ============ 交易所标的数据表设计 ============

-- ************ 交易所标的表 ************
CREATE TABLE IF NOT EXISTS t_exchange_symbols (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,       -- 主键ID
    c_exchange TEXT NOT NULL,                              -- 交易所名称（binance/okx/huobi等）
    c_inst_type TEXT NOT NULL,                             -- 产品类型（SPOT/SWAP/FUTURES等）
    c_symbol TEXT NOT NULL,                                -- 标的符号（BTC-USDT）
    c_base_currency TEXT NOT NULL,                         -- 基础货币（BTC）
    c_quote_currency TEXT NOT NULL,                        -- 计价货币（USDT）
    c_status TEXT NOT NULL DEFAULT 'active',               -- 状态（active=活跃，inactive=停用，delisted=退市）

    -- 交易规则
    c_min_qty TEXT NOT NULL DEFAULT '',                    -- 最小交易数量
    c_max_qty TEXT NOT NULL DEFAULT '',                    -- 最大交易数量
    c_tick_size TEXT NOT NULL DEFAULT '',                  -- 价格最小变动单位
    c_lot_size TEXT NOT NULL DEFAULT '',                   -- 数量最小变动单位

    -- 扩展信息
    c_metadata TEXT NOT NULL DEFAULT '{}',                 -- 扩展元数据（JSON格式）

    -- 同步信息
    c_sync_time INTEGER NOT NULL,                          -- 同步时间戳（毫秒）

    -- 审计字段
    c_invalid INTEGER NOT NULL DEFAULT 0,                  -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,            -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP             -- 修改时间
);

-- ************ 创建交易所标的相关索引 ************
-- 唯一索引：交易所+产品类型+标的
CREATE UNIQUE INDEX IF NOT EXISTS idx_exchange_symbols_unique ON t_exchange_symbols(c_exchange, c_inst_type, c_symbol, c_invalid);

-- 查询索引
CREATE INDEX IF NOT EXISTS idx_exchange_symbols_exchange ON t_exchange_symbols(c_exchange);
CREATE INDEX IF NOT EXISTS idx_exchange_symbols_inst_type ON t_exchange_symbols(c_inst_type);
CREATE INDEX IF NOT EXISTS idx_exchange_symbols_status ON t_exchange_symbols(c_status);
CREATE INDEX IF NOT EXISTS idx_exchange_symbols_sync_time ON t_exchange_symbols(c_sync_time);
CREATE INDEX IF NOT EXISTS idx_exchange_symbols_invalid ON t_exchange_symbols(c_invalid);

-- 联合查询索引
CREATE INDEX IF NOT EXISTS idx_exchange_symbols_exchange_inst ON t_exchange_symbols(c_exchange, c_inst_type, c_status, c_invalid);

-- ************ 创建交易所标的相关触发器 ************
-- 标的表更新触发器
DROP TRIGGER IF EXISTS update_exchange_symbols_mtime;
CREATE TRIGGER update_exchange_symbols_mtime AFTER UPDATE ON t_exchange_symbols BEGIN
    UPDATE t_exchange_symbols SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid; END;
