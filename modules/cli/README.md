# MooX CLI 工具

[![Go Version](https://img.shields.io/badge/Go-1.21+-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS%20%7C%20Windows-lightgrey.svg)](https://github.com/mooyang-code/moox)

## 🎯 简介

MooX CLI 是一个功能强大、稳定可靠的命令行工具，为 MooX 一站式量化管理平台提供完整的客户端功能。经过精心设计的架构确保了出色的用户体验和高度的容错性。

## ✨ 功能特性

- 🔐 **用户认证**: 安全的用户注册、登录，支持交互式密码输入
- 🗄️ **数据库操作**: 创建表、插入数据、查看数据等完整数据库管理
- 🧭 **元数据导入**: 通过 storage MetadataService 导入初始化 seed
- 📦 **存储服务**: 高性能的数据读写操作
- 📨 **消息队列**: 实时消息处理和队列管理
- 🛠️ **云运维**: 腾讯云轻量应用服务器防火墙规则管理
- 🌍 **国际化支持**: 完整的中文界面和命令别名
- 🛡️ **容错性强**: 智能错误处理，永不崩溃
- ⚙️ **灵活配置**: 多路径配置文件支持，环境变量配置

## 🚀 快速开始

### 安装和构建

```bash
# 克隆项目
git clone https://github.com/mooyang-code/moox.git
cd moox/cli

# 构建当前平台
go build -o moox-cli .

# 跨平台构建（可选）
cd scripts
./build.sh v1.0.0
```

### 首次使用

```bash
# 查看版本信息
./moox-cli --version

# 查看帮助信息
./moox-cli --help

# 查看认证功能
./moox-cli auth --help
```

## ⚙️ 配置管理

### 配置文件位置

MooX CLI 支持从多个位置自动加载配置文件（按优先级排序）：

1. 🌍 环境变量: `MOOX_CONFIG=/path/to/config.yaml`
2. 📁 当前目录: `./config/cli.yaml`
3. 📁 当前目录: `./cli.yaml`
4. 📁 上级目录: `./config/cli.yaml`
5. 🏠 用户目录: `~/.moox/cli.yaml`
6. 🖥️ 系统目录: `/etc/moox/cli.yaml`

### 配置文件示例

创建 `config/cli.yaml`：

```yaml
# MooX 认证服务配置
moox:
  auth_target: "127.0.0.1:18200"    # 认证服务地址

# 元数据数据库配置
metadata_database:
  storage_device: "sqlite:./data/metadata.db"

# 存储服务配置
storage:
  target: "127.0.0.1:18102"         # 存储服务地址

# 消息服务配置
message:
  server: "nats:localhost:4222"     # NATS服务器地址
  consumer: "MY_CONSUMER"           # 消费者名称
  subject: "storage.datadetail.change"  # 订阅主题
  max_wait_time: 5000               # 最大等待时间(毫秒)
```

### 环境变量配置

```bash
# 指定自定义配置文件
export MOOX_CONFIG="/path/to/custom/config.yaml"

# 运行CLI
./moox-cli auth register
```

## 🧭 存储元数据初始化

通过 `metadata import` 将领域对象格式的 seed 导入 moox-storage，CLI 会调用 `MetadataService`，不会直接写 SQLite 表：

```bash
./moox-cli metadata import \
  --file ../storage/config/metadata.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists
```

试跑导入计划：

```bash
./moox-cli metadata import --file ../storage/config/metadata.seed.yaml --dry-run
```

## 🛠️ 腾讯云运维

通过腾讯云 Lighthouse `CreateFirewallRules` 接口给轻量应用服务器添加防火墙规则。密钥可通过参数传入，也可使用环境变量 `TENCENTCLOUD_SECRET_ID` / `TENCENTCLOUD_SECRET_KEY`。

```bash
export TENCENTCLOUD_SECRET_ID="AKID..."
export TENCENTCLOUD_SECRET_KEY="..."

./moox-cli ops tencent lighthouse firewall add \
  --region ap-guangzhou \
  --public-ip <lighthouse-public-ip> \
  --ports 20201,20200,11000 \
  --protocol TCP \
  --cidr 0.0.0.0/0 \
  --description "moox services"
```

> `<lighthouse-public-ip>` 真实值见 `infra/infra.local.yaml`。

也可以直接传实例 ID，并先用 `--dry-run` 预览请求：

```bash
./moox-cli ops tencent lighthouse firewall add \
  --instance-id lhins-xxxx \
  --ports 20201,20200,11000 \
  --dry-run
```

## 📥 历史数据导入

通过 `storage import` 将本地历史数据文件导入已登记的 Dataset。命令入口保持稳定，数据格式通过 `--format` 选择；当前支持 `csv`，也可以使用 `auto` 按文件扩展名推断，后续新增 JSONL、Parquet 等格式时会复用同一入口。

```bash
./moox-cli storage import \
  --format csv \
  --file ~/Downloads/ARB-USDT.csv \
  --access-url http://127.0.0.1:20201 \
  --metadata-url http://127.0.0.1:20200 \
  --space crypto \
  --view ar_usdt_close_view \
  --dataset binance_spot_kline_1h \
  --subject AR-USDT \
  --data-source binance \
  --freq 1h \
  --time-column candle_begin_time
```

导入前会读取 MetadataService 校验 Dataset、View 归属、Subject 绑定、列契约和字段类型；CSV 可包含交易所下载横幅，CLI 会跳过横幅并从包含 `--time-column` 的行识别表头。试跑可加 `--dry-run`，只做校验和导入计划输出，不写入数据。

## 🔐 用户认证功能

### 交互式用户注册

最推荐的注册方式，提供友好的交互界面：

```bash
./moox-cli auth register
```

执行效果：
```
🚀 欢迎使用 MooX 用户注册功能！
📡 认证服务地址: 127.0.0.1:18200

👤 请输入用户名: myuser
🔒 请输入密码: ********
🔒 请再次输入密码: ********
✅ 密码确认成功
😊 请输入昵称 (可选, 直接回车跳过): 我的昵称
📧 请输入邮箱 (可选, 直接回车跳过): my@example.com

🔄 正在注册用户 'myuser'...
🎉 注册成功！
👤 用户ID: 123e4567-e89b-12d3-a456-426614174000
📛 用户名: myuser
😊 昵称: 我的昵称
📧 邮箱: my@example.com
✅ 状态: 1
🔰 角色: 1
```

### 命令行参数注册

适合脚本化场景：

```bash
# 基本注册
./moox-cli auth register --username myuser --nickname "我的昵称"

# 完整参数注册（不推荐在命令行中传递密码）
./moox-cli auth register \
  --username myuser \
  --nickname "我的昵称" \
  --email "my@example.com"
```

### 中文命令支持

支持完整的中文命令别名：

```bash
# 中文命令
./moox-cli 认证 注册

# 查看中文帮助
./moox-cli 认证 --help
```

### 参数说明

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `--username` | string | ✅ | 用户名，3-20个字符 |
| `--password` | string | ✅ | 密码，建议交互式输入 |
| `--nickname` | string | ❌ | 用户昵称，可选 |
| `--email` | string | ❌ | 邮箱地址，可选 |

## 🗄️ 数据库操作

```bash
# 查看数据库操作帮助
./moox-cli db --help

# 创建元数据表
./moox-cli db --meta-schema schema.sql

# 创建数据表
./moox-cli db --create-table mytable

# 插入数据
./moox-cli db --insert-data data.json

# 查看表结构
./moox-cli db --show-schema mytable

# 查看表数据
./moox-cli db --show-data mytable
```

## 📦 存储服务

```bash
# 查看存储操作帮助
./moox-cli storage --help

# 存储服务相关操作
# （具体命令请参考帮助信息）
```

## 📨 消息队列

```bash
# 消费消息队列
./moox-cli msg consume

# 查看消息队列帮助
./moox-cli message --help
```

## 🔧 故障排除

### 常见问题解决

#### ❌ 配置文件找不到

**错误信息：**
```
警告：加载配置失败: 无法找到配置文件
```

**解决方案：**
1. 创建配置文件：`mkdir -p config && cp config/cli-example.yaml config/cli.yaml`
2. 使用环境变量：`export MOOX_CONFIG=/path/to/config.yaml`
3. 检查文件权限：`chmod 644 config/cli.yaml`

#### ❌ 认证服务连接失败

**错误信息：**
```
❌ 错误：未配置认证服务地址
```

**解决方案：**
1. 检查配置文件中的 `moox.auth_target` 设置
2. 确保认证服务正在运行：`curl http://127.0.0.1:18200/health`
3. 检查网络连接和防火墙设置

#### ❌ 权限不足

**错误信息：**
```
permission denied: ./moox-cli
```

**解决方案：**
```bash
chmod +x moox-cli
```

#### ❌ Go版本不兼容

**错误信息：**
```
go: module requires Go 1.21
```

**解决方案：**
```bash
# 检查Go版本
go version

# 升级Go（如果需要）
# 请访问 https://golang.org/dl/
```

### 调试模式

启用详细日志输出：

```bash
# 设置调试环境变量
export MOOX_DEBUG=true

# 运行命令
./moox-cli auth register --username debuguser
```

## 🏗️ 技术架构

### 核心组件

- **🎯 命令路由**: 基于 Cobra 框架的现代CLI架构
- **⚙️ 配置管理**: 支持多格式、多路径的灵活配置系统
- **🔐 认证客户端**: 安全的 tRPC 客户端，支持超时和重试
- **🗄️ 数据库抽象**: 支持多种数据库驱动的统一接口
- **📨 消息处理**: 基于 NATS 的高性能消息队列客户端
- **🛡️ 错误处理**: 完善的错误恢复和用户友好的错误提示

### 项目结构

```
cli/
├── cmd/                    # 命令定义
│   ├── root.go            # 根命令和全局配置
│   ├── auth.go            # 认证相关命令
│   ├── database.go        # 数据库操作命令
│   └── message.go         # 消息队列命令
├── internal/              # 内部包
│   ├── config/            # 配置管理
│   ├── auth/              # 认证客户端
│   ├── database/          # 数据库操作
│   └── message/           # 消息处理
├── config/                # 配置文件
│   ├── cli.yaml          # 当前配置
│   └── cli-example.yaml  # 示例配置
├── scripts/               # 构建脚本
└── README.md             # 说明文档
```

### 依赖项

```go
// 核心依赖
github.com/spf13/cobra         // CLI框架
gopkg.in/yaml.v2              // YAML配置解析
golang.org/x/term             // 终端控制
trpc.group/trpc-go/trpc-go    // tRPC客户端

// 数据库驱动
github.com/mattn/go-sqlite3   // SQLite支持

// 消息队列
github.com/nats-io/nats.go    // NATS客户端
```

## 🚢 部署指南

### 开发环境

```bash
# 直接运行
cd cli
go run . auth register

# 或构建后运行
go build -o moox-cli .
./moox-cli auth register
```

### 生产环境

```bash
# 使用构建脚本
cd scripts
./build.sh v1.0.0

# 系统安装
cd ../release
sudo ./install.sh

# 全局使用
moox-cli auth register
```

### Docker部署

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o moox-cli .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/moox-cli .
COPY --from=builder /app/config ./config
CMD ["./moox-cli"]
```

## 📈 最佳实践

### 安全建议

1. **🔒 密码安全**: 始终使用交互式密码输入，避免在命令行中传递密码
2. **📁 配置安全**: 设置适当的配置文件权限 (`chmod 600 config/cli.yaml`)
3. **🌐 网络安全**: 在生产环境中使用HTTPS和适当的认证

### 性能优化

1. **⚡ 连接复用**: 客户端自动处理连接池和复用
2. **🎯 超时设置**: 合理设置请求超时时间
3. **📊 监控**: 使用调试模式监控性能

### 开发建议

1. **📝 配置管理**: 使用环境变量区分开发、测试、生产环境
2. **🧪 测试**: 编写完整的单元测试和集成测试
3. **📚 文档**: 保持README和代码注释的更新

## 🤝 贡献指南

1. Fork 项目
2. 创建功能分支 (`git checkout -b feature/amazing-feature`)
3. 提交更改 (`git commit -m 'Add some amazing feature'`)
4. 推送到分支 (`git push origin feature/amazing-feature`)
5. 开启 Pull Request

## 📄 许可证

本项目采用 MIT 许可证 - 查看 [LICENSE](LICENSE) 文件了解详情。

## 📞 支持

- 📧 邮箱: support@moox.com
- 🐛 问题反馈: [GitHub Issues](https://github.com/mooyang-code/moox/issues)
- 📖 文档: [项目Wiki](https://github.com/mooyang-code/moox/wiki)
- 💬 讨论: [GitHub Discussions](https://github.com/mooyang-code/moox/discussions)

---

**MooX CLI** - 让量化投资决策更智能 🚀📊
