# Pebble 存储适配层说明

storage adapter 的在线有序 KV 引擎使用 CockroachDB Pebble。

## 职责

- 支持静态结构化数据的行级写入、读取和软删除。
- 支持时序数据按 `table_id + row_id + time + field_id` 的有序 key 写入。
- 支持时序范围扫描、最近 N 条读取和基础条件过滤。
- 通过 `dao.Storer` 接口接入 adapter 路由层。

## Key 结构

字段 key：

```text
{table_id}|{row_id}|{time}|f{field_id}
```

表元数据 key：

```text
{table_id}|_table_meta|exists
```

静态行软删除 key：

```text
{table_id}|{row_id}||_meta|deleted
{table_id}|{row_id}||_meta|deleted_time
```

## 配置

```yaml
pebble:
  data_path: "./data/pebble"
```

`localhost` 连接信息会解析为该配置路径。

## 验证

```bash
go test ./internal/services/adapter/dao/pebble
make test
```
