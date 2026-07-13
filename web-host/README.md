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
- `make deploy SERVER=user@host` - 通过 `scripts/deploy-moox.sh` 发布 web-host

## 开发流程

1. 前端开发时在 `web` 目录进行
2. 前端构建完成后，在本目录运行 `make statik`
3. 再运行 `make build`
4. 生成在仓库根目录的 `bin/moox-web-host` 二进制文件包含了所有前端资源

仓库发布脚本默认会重新构建前端并生成 statik 资源：

```bash
cd ..
./scripts/deploy-moox.sh --target user@host --build-web-assets
```

## API 访问方式

Web Host 只负责提供前端静态资源，不代理 API 请求。浏览器只访问 Caddy `https://{当前hostname}:9527`：站点路由由 Caddy 转发到 web-host `127.0.0.1:9528`，`/api/admin/*` 由 Caddy 转发到 Admin control `127.0.0.1:11000`。浏览器不直连 Admin，前端代码禁止调用 `/api/service/*`。

后台/SCF 使用独立 Caddy service edge `https://<host>:11001/api/service/*`，携带 service HMAC 并验证 Caddy CA；流量不经过 web-host 或 browser edge。

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

`/healthz`、`/readyz`、`/metrics` 不在静态监听上暴露；诊断监听缺少有效 `X-Moox-Health-Auth` 时返回 `401`。完整证书流程见 `docs/运维/管理台HTTPS与证书.md`。
