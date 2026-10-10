# 控制台代理（Caddy）HTTPS

控制台代理是组件 `console-proxy`，只部署在 control 上。MooX 固定使用 Caddy `v2.11.4`，由 `moox-cli` 下载、校验后装进发布目录，
以普通用户身份运行，不依赖系统的 Caddy，也不接管系统里已有的 Caddy。

它只有一个站点 `https://<control 公网地址>:9527`：

| 路径 | 上游 |
| --- | --- |
| `/api/admin/*` | 管理后台的控制台 API，`127.0.0.1:11000` |
| 其他 `/api/*`、`/healthz`、`/readyz`、`/metrics` | 直接返回 404 |
| 其余路径 | 控制台前端 web-host，`127.0.0.1:9528` |

两个上游都只监听回环地址。服务之间的调用不经过 Caddy（走主机网关，TLS 由 MooX 私有 CA 签发的证书保护），也没有 11001 站点。

## 证书模式

由 `[hosts.control]` 的 `tls_mode` 决定：

| 值 | 行为 |
| --- | --- |
| `auto`（默认） | 公网 IP/DNS 用 `public`，私网、回环地址和 `.localhost` 用 `internal` |
| `public` | Let's Encrypt `shortlived` 证书，HTTP-01 验证。TCP 80 必须持续对公网开放，用于签发和续期。浏览器使用操作系统信任库，不需要安装任何根证书 |
| `internal` | Caddy 内置 CA。根证书在 control 的 `data/console-proxy/caddy/pki/authorities/local/root.crt`，需要让每台浏览器机器信任 |

Caddy 在运行期间按 ACME ARI 自动续期；每分钟的 `healthcheck.sh` 通过 `https://<地址>:9527/` 探测它，异常时拉起。证书状态保存在
`data/console-proxy/`，普通重新部署和数据重置都不会丢失。`setup firewall` 会在 `public` 模式下同时开放 80。

## internal 模式的根证书

日常用 `moox-cli setup trust-browser --file ./moox.toml` 检查并安装本机浏览器信任（`setup apply` 和 `setup init` 也会先做这个检查）。
需要手工取回、核对指纹时用 `skills/moox/scripts/caddy-ca.sh`：

```bash
skills/moox/scripts/caddy-ca.sh fetch --target user@host --deploy-dir /data/moox/prod \
  --output ~/.moox/certs/moox-caddy-root-<地址>.crt
skills/moox/scripts/caddy-ca.sh inspect --ca-file ~/.moox/certs/moox-caddy-root-<地址>.crt
skills/moox/scripts/caddy-ca.sh install --ca-file ~/.moox/certs/moox-caddy-root-<地址>.crt
skills/moox/scripts/caddy-ca.sh status --ca-file ~/.moox/certs/moox-caddy-root-<地址>.crt
```

`fetch --expected-fingerprint <SHA256 指纹>` 会在指纹不一致时删除下载的文件。`root.key` 必须留在 control 上，脚本拒绝处理私钥。
没有免密 sudo 又不想等待输入时给 `install` 加 `--non-interactive`，缺少授权会以退出码 77 失败。

只有控制台代理使用 internal 证书。后端服务和 SCF 都不需要这张根证书：它们之间的 TLS 信任的是 MooX 私有 CA（每台主机的
`certs/moox-ca.crt`）。不要关闭 TLS 校验。
