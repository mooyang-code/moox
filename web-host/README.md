# MooX Web Host

这是 MooX 项目的 Web 静态文件服务器，使用 Go 和 statik 将前端构建产物嵌入到单个二进制文件中。

## 项目结构

```
web-host/
├── main.go             # 静态资源与管理台 API 网关入口
├── internal/
│   └── statik/         # statik 生成的静态资源
├── go.mod              # Go 模块定义
├── go.sum              # Go 依赖锁定文件
└── Makefile            # 构建脚本
```

## 构建步骤

1. 首先确保前端已构建完成：
   ```bash
   cd ../web
   npm run build
   ```

2. 构建 Web Host：
   ```bash
   make build
   ```

3. 运行服务器：
   ```bash
   make run
   ```

## Makefile 命令

- `make build` - 生成 statik 文件并构建 Go 二进制文件
- `make statik` - 仅生成 statik 文件（前端更新后使用）
- `make clean` - 清理构建产物
- `make run` - 构建并运行服务器
- `make deps` - 下载和整理依赖
- `make install-statik` - 安装 statik 工具

## 开发流程

1. 前端开发时在 `web` 目录进行
2. 前端构建完成后，在本目录运行 `make build`
3. 生成的 `moox-web` 二进制文件包含了所有前端资源

## API 访问方式

Web Host 只负责提供前端静态资源，不再代理 API 请求。浏览器访问管理台时，前端会从当前 URL 读取 hostname，并使用固定网关端口请求后台：

- 管理台请求：`http(s)://{当前hostname}:11000/api/admin/{service}/{method}`
- 后台服务请求：`/api/service/{service}/{method}`，由 SCF / collector 等后台组件调用

`web-host` 收到 `/api/*` 请求会返回 404，用于暴露错误的代理依赖。

默认配置：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MOOX_WEB_HOST_ADDR` | `:10080` | Web Host 监听地址 |

示例：

```bash
MOOX_WEB_HOST_ADDR=:10080 \
make run
```
