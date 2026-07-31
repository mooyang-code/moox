# 管理台 HTTPS 与证书

## 网络拓扑

```text
Browser --HTTPS--> 中央 Caddy --HTTP loopback--> web-host:9528 or Admin:11000
Gateway --HTTPS + control HMAC--> 中央 Caddy --HTTP loopback--> Admin:11000
Backend/SCF --HTTPS + service HMAC--> 节点 Caddy --HTTP loopback--> Gateway:11002
SCF --native tRPC + service HMAC--> 控制节点 Gateway:11003
Monitor --health HMAC--> dedicated internal health ports
```

Caddy 是 HTTP/HTTPS 的唯一公开边缘。中央节点允许站点、`/api/admin/*`、`/api/gateway-control/*` 和本机 `/api/service/*`；`--no-admin` 节点只允许 `/api/service/*`。控制节点另公开 `11003` 原生 tRPC Gateway，强制 Service HMAC 与 Caller ACL，用于 SCF 访问 Storage。其他 API 与诊断路由返回 `404`。web-host 只提供静态文件，不代理 API。端口由 `--browser-https-port` 和 `--service-https-port` 决定；它们相同时由路径完成分流。

| 组件 | 默认监听 | 可见性 | 鉴权/用途 |
| --- | --- | --- | --- |
| 中央 Caddy HTTPS edge | 部署参数决定 | 公开 | 站点、`/api/admin/*`、`/api/gateway-control/*`、本机 `/api/service/*` |
| 普通节点 Caddy HTTPS edge | 部署参数决定 | 公开 | 仅 `/api/service/*` + service HMAC |
| web-host | `127.0.0.1:9528` HTTP | loopback | Caddy 静态上游 |
| Admin control | `127.0.0.1:11000` HTTP | loopback | Caddy browser 上游 |
| Node Gateway | `127.0.0.1:11002` HTTP | loopback | Caddy service 上游/同机调用 |
| Node Gateway native | `0.0.0.0:11003` tRPC | 控制节点公开 | SCF Storage 调用；Service HMAC + Caller ACL |
| Node Gateway diagnostics | `127.0.0.1:11012` HTTP | loopback | `healthz`、`readyz`、`metrics` |
| web-host health | `127.0.0.1:19527` HTTP | 内部 | health HMAC |
| Admin health | `127.0.0.1:11010` HTTP | 内部 | health HMAC |
| 其他模块诊断端口 | 模块配置 | 可配置私网 | health HMAC |

诊断端口在没有有效 `X-Moox-Health-Auth` 时返回 `401`。是否将其他模块端口绑定到私网地址是部署配置契约；不要把它们加入 Caddy 公开路由。

## 首次部署

`make deploy` 必须同时提供稳定 `--node-id`、中央 `--gateway-control-url`、peer CA bundle 以及 control/service key 文件。部署会自动执行 Caddy 前置步骤：下载固定版本、校验官方 checksum、安装到部署目录、启动 loopback 上游、安全启动或 reload MooX 管理的 Caddy，然后做 HTTPS 验收。正常流程不需要也不应预先安装系统 Caddy。

`--tls-mode auto` 是默认值：公网 IP/DNS 使用 Let's Encrypt `shortlived` 证书，私网、loopback 和 `*.localhost` 使用 Caddy internal CA。公网模式要求 TCP 80 持续可从互联网到达，以便完成 HTTP-01 初次签发和后续续期；浏览器与后端直接使用操作系统信任库，不安装 MooX 根证书。只有 internal 模式才执行 `--target-ca`、`--local-ca` 和根证书分发流程。

管理边界是部署目录下的 `bin/caddy`、`config/caddy/Caddyfile`、`run/caddy.pid` 和 `data/caddy`。端口被其他进程占用或发现非 MooX Caddy 时部署失败并保留对方进程。

## 公网证书和自动续期

公网模式使用 Caddy `v2.11.4` 的 ACME `shortlived` profile。证书由浏览器系统信任，SAN 是 `--public-host` 的公网 IP 或 DNS；不生成 `certs/caddy/root.crt`，也不要求用户安装证书。Caddy 常驻运行并根据 ACME Renewal Information 自动续期，MooX 每分钟的 `healthcheck.sh` 会检查并拉起异常的 Caddy。`data/caddy` 在普通重新部署和 `--reset-data` 时保留，因此 ACME 账号、证书和续期状态不会丢失。

```bash
curl https://<public-host>:<browser-port>/
openssl s_client -connect <public-host>:<service-port> -servername <public-host> </dev/null
```

`/api/service/*` 不能用普通 curl 作为成功验收，它还需要合法 service HMAC 和目标 `node_id`。正式签名示例见 [Node Gateway 运维手册](../ops/node-gateway.md)。完整组件边界见[节点服务网关架构](../节点服务网关架构.md)。

## Internal CA 获取和信任

显式 `--tls-mode internal` 时，Caddy 生成私有 CA，公开根证书复制为 `certs/caddy/root.crt`；`root.key` 绝不得离开目标机。此时才使用 `skills/moox/scripts/caddy-ca.sh fetch|inspect|install|install-target`，部署默认通过 `--target-ca auto` 和 `--local-ca auto` 安装信任。每台浏览器机器都需安装该 CA，不要通过跳过 TLS 验证规避警告。

Internal 模式的后端进程使用 `MOOX_SERVICE_GATEWAY_CA_FILE=<deploy>/certs/caddy/root.crt`，无法挂载文件的 SCF 使用等价的 `MOOX_SERVICE_GATEWAY_CA_PEM_B64`。公网模式不得注入这些变量。

## 更新、轮换与故障恢复

- Caddy 在保留 `data/caddy` 且进程持续运行时自动续期。公网模式必须保持 TCP 80 可达。修改 `--public-host` 后重新部署，并用 `openssl s_client` 检查 SAN、发行者和有效期。
- 新配置或 Caddy 启动/reload 验收失败时，部署应回滚到上一个已验证的二进制和 Caddyfile；先检查端口所有者、PID 和 Caddy admin 状态，不要手工终止无关进程。
- Internal CA 轮换会使所有已安装的浏览器信任和后端/SCF CA 配置失效，必须作为显式变更。
- Internal CA 私钥丢失时无法从 `root.crt` 恢复；必须轮换 CA 并重新建立全部信任。

当共享工作树中的部署脚本仍在调整参数名或默认值时，以 `scripts/deploy-moox.sh --help`、`deploy/caddy/Caddyfile` 和同版本部署包为实际配置契约，不应由本文推断尚未落地的自动化。
