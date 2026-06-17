# xData-mini Storage 发布与部署指南

本文档说明如何发布与部署 storage 服务。

## CGO 注意事项

- 本项目使用了 CGO。
- 最佳实践是“编译环境与目标运行环境一致”。
- 例如：Linux 产物建议在 Linux 主机上编译。

## 快速部署（脚本）

在项目根目录执行：

```bash
# 先构建目标平台产物
make -C storage build-linux

# 部署到服务器（自动检测远程平台）
make -C storage deploy SERVER=user@host

# 或直接执行脚本
./storage/scripts/deploy.sh user@host
```

脚本会执行：

- 上传对应平台的 release 包
- 备份旧版本
- 保留 `log/` 和 `database/` 目录
- 使用 `start.sh` 启动服务

## 手动部署

1. 在目标平台构建：

```bash
make -C storage build-linux
```

2. 复制 release 目录到服务器：

```bash
scp -r storage/release/linux user@host:~/xdata-storage
```

3. 远程编辑配置：

```bash
ssh user@host
cd ~/xdata-storage
```

4. 启动服务：

```bash
./start.sh
```

5. 停止服务：

```bash
./stop.sh
```

## 运行时路径

启动脚本会设置：

- `STORAGE_CONFIG_PATH=./config`
- `STORAGE_DATABASE_PATH=./database`

日志输出：

- `./log/app.log`

## 部署目录结构（示例）

```
xdata-storage/
├── bin/
│   └── storage
├── config/
├── database/
├── log/
├── start.sh
└── stop.sh
```
