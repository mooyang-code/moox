# 管理台 HTTPS 与证书

## 网络拓扑

```text
Browser --HTTPS--> Caddy:9527 --HTTP loopback--> web-host:9528 or admin-control:11000
Backend/SCF --HTTPS + service HMAC--> Caddy:11001 --HTTP loopback--> admin-service:11002
Monitor --health HMAC--> dedicated internal health ports
```

Caddy 是唯一公开边缘。`9527` 只允许站点路由和 `/api/admin/*`，`11001` 只允许 `/api/service/*`；其他 API 与诊断路由返回 `404`。web-host 只提供静态文件，不代理 API。

| 组件 | 默认监听 | 可见性 | 鉴权/用途 |
| --- | --- | --- | --- |
| Caddy browser edge | `0.0.0.0:9527` HTTPS | 公开 | 站点 + `/api/admin/*` |
| Caddy service edge | `0.0.0.0:11001` HTTPS | 公开 | `/api/service/*` + service HMAC |
| web-host | `127.0.0.1:9528` HTTP | loopback | Caddy 静态上游 |
| Admin control | `127.0.0.1:11000` HTTP | loopback | Caddy browser 上游 |
| Admin service | `127.0.0.1:11002` HTTP | loopback | Caddy service 上游/同机调用 |
| web-host health | `127.0.0.1:19527` HTTP | 内部 | health HMAC |
| Admin health | `127.0.0.1:11010` HTTP | 内部 | health HMAC |
| 其他模块诊断端口 | 模块配置 | 可配置私网 | health HMAC |

诊断端口在没有有效 `X-Moox-Health-Auth` 时返回 `401`。是否将其他模块端口绑定到私网地址是部署配置契约；不要把它们加入 Caddy 公开路由。

## 首次部署

`make deploy ARGS="--target user@host --dir /home/user/moox/prod --public-host <IP-or-DNS>"` 会自动执行 Caddy 前置步骤：下载固定的 Caddy `v2.11.4`、校验官方 checksum、安装到部署目录、启动 loopback 上游、启动或 reload MooX 管理的 Caddy，然后做 HTTPS 验收。正常流程不需要也不应预先安装系统 Caddy。部署还会以 `--target-ca auto` 安装目标机信任，并以 `--local-ca auto` 检查操作机信任；本机没有安装时会自动安装。根证书默认保存为 `~/.moox/certs/moox-caddy-root-<IP-or-DNS>.crt`，文件名带目标地址，迁移或多服务器并存时可直接区分。若本机缺少管理员权限，部署会明确失败并给出安装命令；只有明确不需要浏览器访问时才使用 `--local-ca skip`。使用 `--target-ca skip` 可显式跳过目标机安装。

管理边界是部署目录下的 `bin/caddy`、`config/caddy/Caddyfile`、`run/caddy.pid` 和 `data/caddy`。端口被其他进程占用或发现非 MooX Caddy 时部署失败并保留对方进程。

## CA 获取和信任

Caddy `tls internal` 生成私有 CA。`data/caddy` 在普通重新部署和数据重置时保留，公开根证书复制为 `certs/caddy/root.crt`；`root.key` 绝不得离开目标机。

```bash
skills/moox/scripts/caddy-ca.sh fetch --target user@host --deploy-dir /home/user/moox/prod --output ~/.moox/certs/moox-caddy-root-<IP-or-DNS>.crt
skills/moox/scripts/caddy-ca.sh inspect --ca-file ~/.moox/certs/moox-caddy-root-<IP-or-DNS>.crt
skills/moox/scripts/caddy-ca.sh install --ca-file ~/.moox/certs/moox-caddy-root-<IP-or-DNS>.crt
curl --cacert ~/.moox/certs/moox-caddy-root-<IP-or-DNS>.crt https://<host>:9527/
curl --cacert ~/.moox/certs/moox-caddy-root-<IP-or-DNS>.crt https://<host>:11001/api/service/sysdeploy/GetServiceDeployment
openssl s_client -connect <host>:9527 -servername <dns-name> -CAfile ~/.moox/certs/moox-caddy-root-<IP-or-DNS>.crt </dev/null
```

`install` 在 macOS 调用 `security add-trusted-cert`，Windows 调用 `Import-Certificate`，Debian/Ubuntu 使用 `update-ca-certificates`，RHEL/Fedora 使用 `update-ca-trust`。每次公开部署都会按 SHA-256 指纹检查操作机信任；缺失时自动安装，非交互部署会要求免密管理员权限，否则失败并提示手工命令。每台浏览器机器都需本地安装 CA，否则浏览器显示不可信警告；不要通过跳过 TLS 验证规避警告。

Skill 也可单独为 web-host 目标机安装信任：`skills/moox/scripts/caddy-ca.sh install-target --target user@host --deploy-dir /home/user/moox/prod`。该命令只读取公开根证书，不复制或导出 `root.key`。

后端进程使用 `MOOX_SERVICE_GATEWAY_CA_FILE=<deploy>/certs/caddy/root.crt`，无法挂载文件的 SCF 使用等价的 `MOOX_SERVICE_GATEWAY_CA_PEM_B64`。这些只包含公开 CA，service HMAC secret 另行注入。

## 更新、轮换与故障恢复

- Caddy 在保留 `data/caddy` 时自动续期叶子证书。修改 `--public-host` 以改变 IP/DNS SAN 后重新部署，并用 `openssl s_client` 检查 SAN 和信任链。
- 新配置或 Caddy 启动/reload 验收失败时，部署应回滚到上一个已验证的二进制和 Caddyfile；先检查端口所有者、PID 和 Caddy admin 状态，不要手工终止无关进程。
- CA 轮换会使所有已安装的浏览器信任和后端/SCF CA 配置失效，必须作为显式变更：备份、生成新 CA、核对指纹、向全部调用方重新分发并完成 HTTPS 验收。
- 如果 CA 私钥丢失，无法从 `root.crt` 恢复；必须轮换 CA 并重新建立全部信任。私钥和密钥文件仅允许部署用户读取，根证书可读但不应与私钥一起打包。

当共享工作树中的部署脚本仍在调整参数名或默认值时，以 `scripts/deploy-moox.sh --help`、`deploy/caddy/Caddyfile` 和同版本部署包为实际配置契约，不应由本文推断尚未落地的自动化。
