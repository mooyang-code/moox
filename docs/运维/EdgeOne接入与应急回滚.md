# EdgeOne 接入与应急回滚

## 范围

仅将浏览器域名转发至 Caddy `:9527`，服务调用域名转发至 Caddy `:11001`。web-host、Admin、健康检查、metrics 和模块 RPC 均不得直接暴露。首期采用 CNAME 接入；变更前记录原 DNS 记录、TTL、源站 IP、安全组、预算告警和回滚负责人。

## 上线步骤

1. 在 EdgeOne 添加已完成 ICP 备案的两个主机名，选择 CNAME 接入，并保留原记录作为回滚值。
2. 在 EdgeOne 配置 HTTPS 证书；源站协议选 HTTPS，分别使用源站 `:9527` 和 `:11001`。首期接受默认源站证书行为；付费套餐启用专用 CA 后，再导入 CA 并开启源站证书校验。
3. 安全组仅允许经过审核的 EdgeOne 回源 IP 段访问两个 Caddy 端口，拒绝所有其他入站端口。审核 IP 段来源、版本和生效时间后，才可生成 Caddy trusted-proxy 配置；在此之前，授权、限流和审计不得相信 `X-Forwarded-*`。
4. 在 EdgeOne 事件中心和费用中心建立 WAF、CC、带宽、请求数与缓存命中率告警，设置预算阈值与通知负责人。
5. 先在 staging 切换 CNAME，等待证书可用后运行外部探测。确认 DNS TTL 内可以恢复原记录，再切 production。

## 规则发布

以下规则先观察一天有效流量，再分别升级为拦截，避免把登录、CLI 或 SCF 调用一次性误伤。

| 规则 | 路径 | 初始动作 |
|---|---|---|
| 登录 | `/api/admin/auth/login` | 每客户端 IP 10 次/分钟，JavaScript challenge |
| 管理 API | `/api/admin/*`，不含登录 | 每客户端 IP 120 次/分钟，先记录后拦截 |
| 服务 API | `/api/service/*` | 每客户端 IP 120 次/分钟，先记录后拦截 |
| 静态资源 | JS、CSS、字体、图片 | EdgeOne 缓存，不设应用层严限流 |
| 诊断 | health、ready、metrics | 直接拦截 |

WAF 使用托管规则、CC/Bot 防护与速率规则；认证和请求签名仍由 MooX 网关负责。不要把 WAF 规则当作业务幂等、鉴权或服务熔断的替代品。

## 验收与回滚

上线前后执行：

```bash
bash scripts/test-edgeone-origin-contract.sh
bash scripts/test-caddy-config.sh
curl -fsS https://$MOOX_PUBLIC_HOST:9527/ -o /dev/null
curl -fsS http://$MOOX_PUBLIC_HOST:9528/ && exit 1 || true
curl -fsS http://$MOOX_PUBLIC_HOST:11000/healthz && exit 1 || true
```

受保护 API 必须使用有效签名请求验证，并把认证失败和网络路由失败分别记录。发生误拦截、源站错误激增或成本异常时：先把新规则切回观察/关闭，再在记录的 TTL 内恢复原 CNAME 记录；保留 EdgeOne 事件、Caddy access/error 日志和变更时间，确认源站未开放后再复盘。
