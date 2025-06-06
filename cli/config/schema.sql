-- ************ 创建项目定义表 ************
CREATE TABLE t_project (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_proj_id INTEGER NOT NULL DEFAULT 0,
    c_proj_name TEXT NOT NULL DEFAULT '',
    c_remark TEXT,
    c_is_hide INTEGER NOT NULL DEFAULT 0,
    c_invalid INTEGER NOT NULL DEFAULT 0,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (c_proj_id)
);

-- 创建索引
CREATE INDEX idx_proj_name ON t_project (c_proj_name);
CREATE INDEX idx_ctime ON t_project (c_ctime);
CREATE INDEX idx_mtime ON t_project (c_mtime);

-- 创建触发器，更新修改时间
CREATE TRIGGER update_project_mtime AFTER UPDATE ON t_project
BEGIN
    UPDATE t_project SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;


-- ************ 创建数据集定义表 ************
CREATE TABLE t_dataset (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_dataset_id INTEGER NOT NULL DEFAULT 0, -- 数据集ID
    c_dataset_name TEXT NOT NULL DEFAULT '', -- 数据集名
    c_proj_id INTEGER NOT NULL DEFAULT 0, -- 所属项目ID
    c_data_type INTEGER NOT NULL DEFAULT 0, -- 数据类型（取值见:common.proto-EnumDataType）
    c_freqs TEXT DEFAULT '', -- 时序周期（多值用+分割）
    c_check_rules TEXT NOT NULL DEFAULT '', -- 数据完整性校验规则（内置的校验规则名）
    c_comment TEXT, -- 备注
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    UNIQUE (c_dataset_id)
);

-- 创建索引
CREATE INDEX idx_dataset_name ON t_dataset (c_dataset_name);
CREATE INDEX idx_data_type ON t_dataset (c_data_type);
CREATE INDEX idx_proj_id ON t_dataset (c_proj_id);
CREATE INDEX idx_ctime ON t_dataset (c_ctime);
CREATE INDEX idx_mtime ON t_dataset (c_mtime);

-- 创建触发器，更新修改时间
CREATE TRIGGER update_dataset_mtime AFTER UPDATE ON t_dataset
BEGIN
    UPDATE t_dataset SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;


-- ************ 创建字段定义表 ************
CREATE TABLE t_field (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 主键ID
    c_field_id INTEGER NOT NULL DEFAULT 0, -- 字段ID
    c_proj_id INTEGER NOT NULL DEFAULT 0, -- 所属项目ID
    c_dataset_ids TEXT NOT NULL DEFAULT '', -- 关联的数据集ID（多个用+分隔）
    c_field_name TEXT NOT NULL DEFAULT '', -- 字段中文名
    c_interface_name TEXT NOT NULL DEFAULT '', -- 字段英文名（对外接口名）
    c_data_category INTEGER NOT NULL DEFAULT 0, -- 字段所属数据类型（1静态字段，2时序字段；与pb.EnumDataTypeCategory一致）
    c_desc TEXT NOT NULL DEFAULT '', -- 字段简要描述
    c_is_meta INTEGER NOT NULL DEFAULT 0, -- 是否数据对象元数据字段（0否，1是）
    c_is_required INTEGER NOT NULL DEFAULT 0, -- 是否必填（0否，1是）
    c_is_unique INTEGER NOT NULL DEFAULT 0, -- 是否唯一（0否，1是）
    c_dataset_override TEXT NOT NULL DEFAULT '', -- 数据集特殊干预配置json
    c_parent_field_id INTEGER NOT NULL DEFAULT 0, -- 父字段ID
    c_level_info TEXT NOT NULL DEFAULT '', -- 字段等级信息
    c_field_primary_format INTEGER NOT NULL DEFAULT 0, -- 字段一级格式
    c_field_secondary_format INTEGER NOT NULL DEFAULT 0, -- 字段二级格式
    c_value_lib_id INTEGER NOT NULL DEFAULT 0, -- 属性值库ID
    c_validation_rule TEXT NOT NULL DEFAULT '', -- 写入校验规则
    c_write_example TEXT NOT NULL DEFAULT '', -- 写入示例
    c_remark TEXT, -- 备注
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记（1字段已失效）
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    UNIQUE (c_field_id),
    UNIQUE (c_interface_name,c_proj_id)
);

-- 创建索引
CREATE INDEX idx_proj_id ON t_field (c_proj_id);
CREATE INDEX idx_field_name ON t_field (c_field_name);
CREATE INDEX idx_ctime ON t_field (c_ctime);
CREATE INDEX idx_mtime ON t_field (c_mtime);

-- 创建触发器，更新修改时间
CREATE TRIGGER update_field_mtime AFTER UPDATE ON t_field
BEGIN
    UPDATE t_field SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;


-- ************ 创建数据对象路由表 ************
CREATE TABLE t_object_route (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 自增ID
    c_dataset_id INTEGER NOT NULL, -- 数据集ID
    c_object_id TEXT NOT NULL, -- 数据对象ID（*表示默认值）
    c_entity_id INTEGER NOT NULL, -- 存储实体ID
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    UNIQUE (c_object_id, c_dataset_id)
);

-- 创建普通索引
CREATE INDEX idx_store_entity_id ON t_object_route (c_entity_id);
CREATE INDEX idx_dataset_id ON t_object_route (c_dataset_id);
CREATE INDEX idx_ctime ON t_object_route (c_ctime);
CREATE INDEX idx_mtime ON t_object_route (c_mtime);

-- 创建触发器，更新修改时间
CREATE TRIGGER update_object_route_mtime AFTER UPDATE ON t_object_route
BEGIN
    UPDATE t_object_route SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;


-- ************ 创建字段路由表 ************
CREATE TABLE t_field_route (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 自增ID
    c_entity_id INTEGER NOT NULL, -- 存储实体ID
    c_field_id INTEGER NOT NULL DEFAULT 0, -- 字段ID
    c_data_category INTEGER NOT NULL DEFAULT 0, -- 字段所属数据类型（1静态字段，2时序字段；与pb.EnumDataTypeCategory一致）若c_field_id有值则以c_field_id为准。c_data_category有值，则表示该类型的所有字段。
    c_device_id INTEGER NOT NULL DEFAULT 0, -- 字段的存储设备ID
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    UNIQUE (c_field_id, c_entity_id, c_data_category)
);

-- 创建普通索引
CREATE INDEX idx_entity_id ON t_field_route (c_entity_id);
CREATE INDEX idx_ctime ON t_field_route (c_ctime);
CREATE INDEX idx_mtime ON t_field_route (c_mtime);

-- 创建触发器，更新修改时间
CREATE TRIGGER update_field_route_mtime AFTER UPDATE ON t_field_route
BEGIN
    UPDATE t_field_route SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;


-- ************ 创建存储实体表 ************
CREATE TABLE t_storage_entity (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 自增ID
    c_entity_id INTEGER NOT NULL DEFAULT 0, -- 存储实体ID
    c_entity_srv_conn TEXT NOT NULL DEFAULT '', -- 存储实体的连接信息（存储层服务的访问地址）
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    UNIQUE (c_entity_id)
);

-- 创建普通索引
CREATE INDEX idx_ctime ON t_storage_entity (c_ctime);
CREATE INDEX idx_mtime ON t_storage_entity (c_mtime);

-- 创建触发器，更新修改时间
CREATE TRIGGER update_storage_entity_mtime AFTER UPDATE ON t_storage_entity
BEGIN
    UPDATE t_storage_entity SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;


-- ************ 创建存储设备表 ************
CREATE TABLE t_storage_device (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, -- 自增ID
    c_device_id INTEGER NOT NULL DEFAULT 0, -- 存储设备ID
    c_device_name TEXT NOT NULL DEFAULT '0', -- 存储设备名
    c_device_type INTEGER NOT NULL DEFAULT 0, -- 存储设备类型（DuckDB、MySQL等，pb.EnumDeviceType中定义）
    c_conn_info TEXT NOT NULL DEFAULT '', -- 存储连接信息（存储设备的访问地址，可以是本地设备，也可以是远程设备。格式dsn://writeuser:passwdxxxx@tcp(127.0.0.1:23925)/d_meta?charset=utf8mb4
    c_invalid INTEGER NOT NULL DEFAULT 0, -- 删除标记
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 创建时间
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP, -- 修改时间
    UNIQUE (c_device_id)
);

-- 创建普通索引
CREATE INDEX idx_ctime ON t_storage_device (c_ctime);
CREATE INDEX idx_mtime ON t_storage_device (c_mtime);

-- 创建触发器，更新修改时间
CREATE TRIGGER update_storage_device_mtime AFTER UPDATE ON t_storage_device
BEGIN
    UPDATE t_storage_device SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;

