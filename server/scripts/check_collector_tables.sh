#!/bin/bash

# 检查采集器相关表是否存在的脚本

DB_PATH="../data/moox.db"

echo "检查数据库文件是否存在..."
if [ ! -f "$DB_PATH" ]; then
    echo "错误: 数据库文件不存在: $DB_PATH"
    echo "请确保数据库已初始化"
    exit 1
fi

echo "数据库文件存在: $DB_PATH"
echo ""

echo "检查表结构..."
echo "========================"

# 检查表是否存在
tables=("t_cloud_nodes" "t_collector_task_config" "t_collector_task_instances")

for table in "${tables[@]}"; do
    echo -n "检查表 $table ... "
    result=$(sqlite3 "$DB_PATH" "SELECT name FROM sqlite_master WHERE type='table' AND name='$table';")
    if [ -z "$result" ]; then
        echo "不存在 ❌"
    else
        echo "存在 ✓"
        # 显示表结构
        echo "  列信息:"
        sqlite3 "$DB_PATH" "PRAGMA table_info($table);" | sed 's/^/    /'
    fi
    echo ""
done

echo "========================"
echo "检查完成"