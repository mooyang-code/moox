# Moox CLI 工具

## 简介

Moox CLI 是一个多功能的命令行工具，支持数据库操作、存储服务和用户认证等功能。

## 功能特性

- **认证功能**: 用户注册、登录等操作
- **数据库操作**: 创建表、插入数据、查看数据等
- **存储服务**: 读写存储服务数据
- **消息队列**: 消息队列相关操作

## 安装和构建

```bash
# 克隆项目
git clone https://github.com/mooyang-code/moox.git
cd moox/cli

# 构建
go build -o moox .
```

## 配置

确保 `config/cli.yaml` 文件包含正确的服务配置：

```yaml
moox: 
  auth_target: "127.0.0.1:18200"  # 认证服务地址

storage: 
  target: "127.0.0.1:18102"       # 存储服务地址

# 其他配置...
```

## 用户认证功能

### 用户注册

#### 交互式注册
```bash
./moox auth register
```

程序会提示您输入：
- 用户名（必填）
- 密码（必填，输入时不显示）
- 昵称（可选）
- 邮箱（可选）

#### 命令行参数注册
```bash
./moox auth register --username myuser --password mypass --nickname "我的昵称" --email my@example.com
```

#### 参数说明
- `--username`: 用户名（必填）
- `--password`: 密码（必填）
- `--nickname`: 昵称（可选）
- `--email`: 邮箱（可选）

#### 中文别名支持
```bash
./moox 认证 注册 --username myuser --password mypass
```

### 示例输出

成功注册时的输出：
```
正在注册用户 'myuser'...
注册成功！
用户ID: 123e4567-e89b-12d3-a456-426614174000
用户名: myuser
昵称: 我的昵称
邮箱: my@example.com
状态: 1
角色: 1
```

失败时的输出：
```
注册失败: 用户名已存在 (错误码: 1)
```

## 其他功能

### 查看帮助
```bash
./moox --help           # 查看所有命令
./moox auth --help      # 查看认证相关命令
./moox auth register --help  # 查看注册命令帮助
```

### 数据库操作
```bash
./moox db --help
```

### 存储服务操作
```bash
./moox storage --help
```

## 技术实现

- **认证客户端**: 使用HTTP客户端直接调用认证服务API，避免protobuf冲突
- **配置管理**: 支持YAML格式配置文件
- **交互式界面**: 支持安全的密码输入（不回显）
- **国际化**: 支持中文命令别名

## 错误处理

常见错误及解决方法：

1. **连接错误**: 确保认证服务正在运行且地址配置正确
2. **配置错误**: 检查 `config/cli.yaml` 文件是否存在且格式正确
3. **权限错误**: 确保有执行权限：`chmod +x moox`

## 开发说明

- 认证服务协议定义在 `../server/proto/moox.proto`
- 配置结构定义在 `internal/config/config.go`
- 认证客户端实现在 `internal/auth/auth.go`
- 命令定义在 `cmd/auth.go`
