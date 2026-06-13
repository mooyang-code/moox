# Node Exporter

Node Exporter 是 Prometheus 生态系统中用于采集硬件和操作系统指标的工具。

## 项目来源

原始项目地址：https://github.com/prometheus/node_exporter.git

## 下载安装

### 下载最新版本（以 Linux amd64 为例）

```bash
# 下载
wget https://github.com/prometheus/node_exporter/releases/download/v1.10.2/node_exporter-1.10.2.linux-amd64.tar.gz

# 解压
tar -xzf node_exporter-1.10.2.linux-amd64.tar.gz
cd node_exporter-1.10.2.linux-amd64

# 运行（前台测试）
./node_exporter

# 访问测试
curl http://localhost:9100/metrics
```

## 源码编译

### 交叉编译 Linux 版本（在 macOS/其他系统上）

```bash
GOOS=linux GOARCH=amd64 make build
```

### 编译其他架构

```bash
# Linux ARM64
GOOS=linux GOARCH=arm64 make build

# Linux ARM v7
GOOS=linux GOARCH=arm GOARM=7 make build

# Linux 386
GOOS=linux GOARCH=386 make build
```

### 本地编译

```bash
make build
```

## 生产部署（Systemd 服务）

### 1. 复制二进制文件

```bash
sudo cp node_exporter /usr/local/bin/
```

### 2. 创建 systemd 服务文件

```bash
sudo vim /etc/systemd/system/node_exporter.service
```

服务文件内容：

```ini
[Unit]
Description=Node Exporter
After=network.target

[Service]
Type=simple
User=nobody
ExecStart=/usr/local/bin/node_exporter \
  --web.listen-address=:9100 \
  --collector.disable-defaults \
  --collector.cpu \
  --collector.meminfo \
  --collector.filesystem \
  --collector.netdev \
  --collector.loadavg
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### 3. 启动服务

```bash
# 重载 systemd 配置
sudo systemctl daemon-reload

# 启动服务
sudo systemctl start node_exporter

# 设置开机自启
sudo systemctl enable node_exporter
```

### 4. 查看状态

```bash
sudo systemctl status node_exporter
```

## 请求方式

### 获取所有指标

```bash
curl http://localhost:9100/metrics
```

### 指定主机和端口

```bash
curl http://host:9100/metrics
```

### 查看健康状态

```bash
curl http://localhost:9100/health
```

### 查看版本信息

```bash
curl http://localhost:9100/
```

## 采集器说明

服务文件中启用的采集器：

- `--collector.disable-defaults` - 禁用默认采集器
- `--collector.cpu` - CPU 使用统计
- `--collector.meminfo` - 内存使用统计
- `--collector.filesystem` - 文件系统统计
- `--collector.netdev` - 网络设备统计
- `--collector.loadavg` - 系统负载统计

## 常用指标说明

- `node_cpu_seconds_total` - CPU 使用时间
- `node_memory_MemTotal_bytes` - 总内存
- `node_memory_MemAvailable_bytes` - 可用内存
- `node_disk_io_time_seconds_total` - 磁盘 IO 时间
- `node_network_receive_bytes_total` - 网络接收字节数
- `node_network_transmit_bytes_total` - 网络发送字节数
- `node_filesystem_size_bytes` - 文件系统大小
- `node_load1` - 1 分钟负载
- `node_load5` - 5 分钟负载
- `node_load15` - 15 分钟负载

## 与 Prometheus 集成

在 Prometheus 配置文件中添加：

```yaml
scrape_configs:
  - job_name: 'node_exporter'
    static_configs:
      - targets: ['localhost:9100']
```

## 更多信息

- 官方文档：https://prometheus.io/docs/guides/node-exporter/
- GitHub 仓库：https://github.com/prometheus/node_exporter
