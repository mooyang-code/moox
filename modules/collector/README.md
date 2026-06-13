# 量化数据采集器

## 项目概述

一个高性能、可扩展的多源数据采集系统，支持市场数据、社交媒体、新闻、链上数据等多种数据源。

## 特性

- 🚀 **高性能**：支持并发采集、批量处理、缓存优化
- 🔌 **插件化架构**：新数据源和采集器可作为插件动态加入
- 📡 **事件驱动**：基于事件总线的松耦合架构
- ⚙️ **配置驱动**：通过配置文件控制系统行为
- 📊 **多源数据采集**：支持市场、社交、新闻、链上等多种数据源
- 🔧 **易于扩展**：清晰的接口定义，方便添加新的数据源

## 架构概览

```
data-collector/
├── cmd/collector/          # 主程序入口
├── configs/               # 配置文件
├── internal/
│   ├── core/             # 核心框架
│   │   ├── app/         # App层（数据源应用）
│   │   ├── collector/   # 采集器层
│   │   └── event/       # 事件系统
│   ├── model/           # 数据模型
│   │   ├── common/      # 通用模型
│   │   └── market/      # 市场数据模型
│   ├── source/          # 数据源实现
│   │   └── market/      # 市场数据源
│   │       └── binance/ # 币安交易所
│   └── storage/         # 存储层
└── pkg/                 # 公共包
```

## 快速开始

### 1. 安装依赖

```bash
go mod download
```

### 2. 编译项目

```bash
go build -o bin/collector cmd/collector/main.go
```

### 3. 配置文件

创建或修改 `configs/config.yaml`：

```yaml
# 主配置文件
system:
  name: "multi-source-data-collector"
  version: "2.0.0"
  environment: "development"
  timezone: "UTC"

# 日志配置
logging:
  level: "info"
  format: "json"
  output: 
    - type: "console"
      level: "info"

# 事件总线配置
event_bus:
  type: "memory"
  buffer_size: 10000
  workers: 10

# 数据源配置
sources:
  market:
    - name: "binance"
      enabled: true
      config: "configs/sources/market/binance.yaml"
```

数据源配置文件 `configs/sources/market/binance.yaml`：

```yaml
# Binance数据源配置
app:
  id: "binance"
  name: "币安交易所"
  description: "币安现货市场数据采集"
  type: "market"

# API配置
api:
  base_url: "https://api.binance.com"

# 采集器配置
collectors:
  kline:
    enabled: true
    symbols: 
      - "BTCUSDT"
      - "ETHUSDT"
    intervals:
      - "1m"
      - "5m"
```

### 4. 运行采集器

```bash
./bin/collector --config configs/config.yaml
```

## 开发指南

### 添加新的数据源

1. 在 `internal/source/{category}/{name}/` 创建数据源目录
2. 实现 App 接口
3. 实现采集器
4. 使用 `init()` 函数自注册

示例：
```go
// internal/source/market/newexchange/app.go
func init() {
    app.RegisterCreator("newexchange", "新交易所", "描述", 
        app.SourceTypeMarket, NewApp)
}
```

### 添加新的采集器

1. 实现 Collector 接口
2. 使用构建器模式注册

示例：
```go
func init() {
    collector.NewBuilder().
        Source("binance", "币安").
        DataType("kline", "K线").
        MarketType("spot", "现货").
        Creator(NewKlineCollector).
        Register()
}
```

## 架构文档

详细的架构设计文档请参考：[docs/architecture.md](docs/architecture.md)

## 构建与部署

### 构建

```bash
make build
```

### 测试

```bash
make test
```

### Docker 部署

```bash
docker build -t data-collector .
docker run -v ./configs:/app/configs data-collector
```

## 贡献指南

欢迎提交 Pull Request 或创建 Issue。

## 许可证

MIT License
