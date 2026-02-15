# 监控服务配置说明

## 配置文件位置

主配置文件：`config/app.yaml`

## 监控配置项

```yaml
monitor:
  node_exporter_port: 9100  # Node Exporter 端口
  collect_timeout: 10       # 采集超时时间（秒）
  concurrent_limit: 20      # 并发采集限制
```

## 配置项说明

### node_exporter_port
- **类型**: int
- **默认值**: 9100
- **说明**: Node Exporter 监听的端口号
- **环境变量**: `NODE_EXPORTER_PORT`
- **示例**:
  ```yaml
  node_exporter_port: 9100
  ```

### collect_timeout
- **类型**: int
- **默认值**: 10
- **说明**: HTTP 请求采集指标的超时时间（秒）
- **环境变量**: `MONITOR_COLLECT_TIMEOUT`
- **建议**:
  - 本地网络：5-10 秒
  - 跨区域网络：15-30 秒
- **示例**:
  ```yaml
  collect_timeout: 10
  ```

### concurrent_limit
- **类型**: int
- **默认值**: 20
- **说明**: 并发采集的最大主机数量，控制每批次同时采集的主机数
- **环境变量**: `MONITOR_CONCURRENT_LIMIT`
- **建议**:
  - 小规模部署（<50 台主机）: 20
  - 中等规模部署（50-200 台主机）: 50
  - 大规模部署（>200 台主机）: 100
- **注意**: 过高的并发可能导致网络拥塞或服务器负载过高
- **示例**:
  ```yaml
  concurrent_limit: 20
  ```

## 环境变量配置

如果不想修改配置文件，可以通过环境变量覆盖配置：

```bash
# Node Exporter 端口
export NODE_EXPORTER_PORT=9100

# 采集超时时间（秒）
export MONITOR_COLLECT_TIMEOUT=10

# 并发采集限制
export MONITOR_CONCURRENT_LIMIT=20
```

## 配置优先级

配置加载优先级（从高到低）：
1. 环境变量
2. 配置文件 `config/app.yaml`
3. 代码中的默认值

## 部署建议

### Node Exporter 部署

在需要监控的主机上安装 Node Exporter：

```bash
# 下载 Node Exporter
wget https://github.com/prometheus/node_exporter/releases/download/v1.7.0/node_exporter-1.7.0.linux-amd64.tar.gz

# 解压
tar xvfz node_exporter-1.7.0.linux-amd64.tar.gz

# 运行（默认端口 9100）
cd node_exporter-1.7.0.linux-amd64
./node_exporter &
```

### 使用 systemd 管理 Node Exporter

创建 systemd 服务文件 `/etc/systemd/system/node_exporter.service`：

```ini
[Unit]
Description=Node Exporter
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/node_exporter
Restart=always

[Install]
WantedBy=multi-user.target
```

启动服务：

```bash
systemctl daemon-reload
systemctl enable node_exporter
systemctl start node_exporter
```

### 防火墙配置

确保 Node Exporter 端口可被监控服务器访问：

```bash
# CentOS/RHEL
firewall-cmd --permanent --add-port=9100/tcp
firewall-cmd --reload

# Ubuntu/Debian
ufw allow 9100/tcp
```

## 性能调优

### 采集超时时间

- 如果经常出现超时错误，可以适当增加 `collect_timeout`
- 监控日志中的采集时间，如果平均采集时间接近超时值，应该增加超时时间

### 并发限制

- 根据服务器性能和网络带宽调整 `concurrent_limit`
- 监控服务器 CPU 和网络 I/O，如果资源充足，可以增加并发数
- 如果发现采集失败率高，可能是并发过高，应该降低并发数

## 监控指标

定时任务每 10 秒采集一次，采集的指标包括：

- **CPU**: 使用率、核心数
- **内存**: 总量、已用、可用、使用率
- **磁盘**: 总量、已用、使用率（根分区）
- **网络**: 接收/发送速率（主网卡）
- **负载**: 1分钟、5分钟、15分钟平均负载

## 故障排查

### 采集失败

检查清单：
1. Node Exporter 是否运行：`curl http://<host>:9100/metrics`
2. 网络连通性：`telnet <host> 9100`
3. 防火墙配置：确保端口开放
4. 配置是否正确：检查 `app.yaml` 中的端口配置
5. 超时时间是否足够：查看日志中的采集时间

### 查看日志

```bash
# 查看监控服务日志
tail -f log/moox.log | grep Monitor
```

### 测试连通性

通过 API 测试 Node Exporter 连通性：

```bash
# 测试主机连通性（host_id 为主机ID）
curl -X POST http://localhost:20103/api/v1/monitor/test/<host_id>
```

## 示例配置

### 生产环境配置

```yaml
monitor:
  node_exporter_port: 9100
  collect_timeout: 15       # 跨区域网络，增加超时
  concurrent_limit: 50      # 中等规模，增加并发
```

### 开发环境配置

```yaml
monitor:
  node_exporter_port: 9100
  collect_timeout: 5        # 本地网络，减少超时
  concurrent_limit: 10      # 减少并发，降低负载
```

### 高性能配置

```yaml
monitor:
  node_exporter_port: 9100
  collect_timeout: 30
  concurrent_limit: 100     # 大规模部署，高并发
```
