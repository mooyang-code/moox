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

2. 构建 Web Host（`build.sh` 会按 `web/dist` 现场生成内嵌资源，不用仓库里提交的 statik.go；第 1 步已构建好前端，所以设 `MOOX_WEB_DIST_READY=1` 跳过 Docker 构建）：
   ```bash
   cd ..
   MOOX_WEB_DIST_READY=1 bash scripts/build/build.sh web-host
   ```

3. 运行服务器：
   ```bash
   MOOX_WEB_HOST_ADDR=127.0.0.1:9528 MOOX_WEB_HOST_HEALTH_ADDR=127.0.0.1:19527 ../bin/moox-web-host
   ```

## Makefile 命令

- `make build` - 调用 `scripts/build/build.sh web-host`：先在 Docker 里按当前源码构建前端，再内嵌并编译（不用仓库里提交的 statik.go）
- `make statik` - 用 `web/dist` 改写仓库里的 `internal/statik/statik.go`（只为让这份提交的文件跟上前端；发布构建不依赖它）
- `make build-linux` / `make build-darwin` - 交叉构建到仓库根目录 `bin/moox-web-host`
- `make clean` - 仅删除 `web-host/bin` 与 `web-host/release`；不会删除根目录 `bin/moox-web-host`
- `make deps` - 执行 `go mod download` 下载依赖（不执行 `go mod tidy`）
- `make lint` - `go vet ./...`

## 开发流程

1. 前端开发时在 `web` 目录进行
2. 在本目录运行 `make build`（或 `make build-linux`），它会按当前源码构建前端并内嵌
3. 生成在仓库根目录的 `bin/moox-web-host` 二进制文件包含了所有前端资源

原生部署（`setup bootstrap` / `setup release build`）会在本机重新构建前端并嵌入 web-host；发布线上二进制见
`skills/moox/references/native-release.md`。

## API 访问方式

Web Host 只负责提供前端静态资源，不代理 API 请求。浏览器只访问控制台代理 `https://{当前hostname}:9527`：站点路由由控制台代理转发到 web-host `127.0.0.1:9528`，`/api/admin/*` 转发到 Admin。浏览器不直连 Admin。

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
