# MooX Web Host

MooX 管理台静态文件服务器。它使用 Go + statik 将 `web/dist` 嵌入到 `moox-web-host` 单个二进制中，只负责前端静态资源和 SPA 路由回退，不代理 `/api/*`。

## 项目结构

```
web-host/
├── main.go             # 前端静态资源入口
├── internal/
│   └── statik/         # statik 生成的静态资源
├── go.mod              # Go 模块定义
├── go.sum              # Go 依赖锁定文件
└── Makefile            # 构建脚本
```

## 构建步骤

1. 先构建前端：
   ```bash
   cd ../web
   pnpm build:prod
   ```

2. 重新生成嵌入静态资源：
   ```bash
   cd ../web-host
   make statik
   ```

3. 构建 Web Host：
   ```bash
   make build
   ```

4. 运行服务器：
   ```bash
   MOOX_WEB_HOST_ADDR=127.0.0.1:9528 MOOX_WEB_HOST_HEALTH_ADDR=127.0.0.1:19527 ../bin/moox-web-host
   ```

## Makefile 命令

- `make build` - 使用当前已嵌入的 statik 文件构建 Go 二进制文件
- `make statik` - 仅生成 statik 文件（前端更新后使用）
- `make build-linux` / `make build-darwin` - 交叉构建到仓库根目录 `bin/moox-web-host`
- `make clean` - 清理构建产物
- `make deps` - 下载和整理依赖
- `make lint` - `go vet ./...`
- `make deploy` - 按 `moox.toml` 的部署表重新部署 web-host（`moox-cli setup deploy-service --component web-host`）

## 开发流程

1. 前端开发时在 `web` 目录进行
2. 前端构建完成后，在本目录运行 `make statik`
3. 再运行 `make build`
4. 生成在仓库根目录的 `bin/moox-web-host` 二进制文件包含了所有前端资源

部署命令使用当前已嵌入的静态资源，所以前端更新后要先完成前两步，再部署：

```bash
cd ..
moox-cli setup deploy-service --component web-host
```

## API 访问方式

Web Host 只负责提供前端静态资源，不代理 API 请求。浏览器只访问控制台代理（Caddy）`https://{当前hostname}:9527`：页面请求转发到 web-host `127.0.0.1:9528`，`/api/admin/*` 转发到管理后台的控制台 API `127.0.0.1:11000`。浏览器不直连其他服务。

`web-host` 收到 `/api/*` 请求会返回 404，用于暴露错误的代理依赖。

默认配置：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MOOX_WEB_HOST_ADDR` | `127.0.0.1:9528` | Caddy 静态上游，只绑定 loopback |
| `MOOX_WEB_HOST_HEALTH_ADDR` | `127.0.0.1:19527` | 独立诊断监听，需 health HMAC |

仓库根目录运行示例：

```bash
MOOX_WEB_HOST_ADDR=127.0.0.1:9528 MOOX_WEB_HOST_HEALTH_ADDR=127.0.0.1:19527 ./bin/moox-web-host
```

`/healthz`、`/readyz`、`/metrics` 不在静态监听上暴露；诊断监听缺少有效 `X-Moox-Health-Auth` 时返回 `401`。证书流程见[部署与运维](../docs/部署与运维.md)，前端说明见[前端](../docs/模块/前端.md)。
