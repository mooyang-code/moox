# MooX Storage 构建说明

storage 模块现在使用 Pebble 作为在线有序 KV 引擎，不需要安装或链接外部 C++ KV 库。

## 常用命令

```bash
make deps
make proto
make test
make build
```

默认构建输出：

```text
bin/moox-storage
```

## 测试

```bash
make test
```

该命令会运行模块内所有 Go 测试，并生成 `cover.out.tmp`。Pebble PrimaryStore 测试覆盖：

- 建表和表存在性检查
- 静态行写入、读取和软删除
- 时序数据写入和范围查询

## 发布

```bash
make release
```

macOS 会生成 `release/darwin`，Linux 会生成 `release/linux`。发布目录包含：

- `bin/moox-storage`
- `config/`
- `log/`
- `database/`
- `start.sh`
- `stop.sh`

## 配置

`config/storage.yaml` 中的在线主存配置为：

```yaml
storage:
  devices:
    pebble_path: ./var/storage/pebble
```

当在线主存服务使用本地模式时，会使用该路径。
