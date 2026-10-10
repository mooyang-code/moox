# 出口代理

`moox-egress-proxy` 提供 `trpc.moox.egress.Proxy`，原生 tRPC 仅监听 loopback 11440。主机网关按组件目录验证内部签名与方法权限，仅允许 Collector 调用；无需额外公网监听或代理凭据。

`Do` 只发送 HTTPS GET/POST。`config/app.yaml` 的 `domains` 支持精确 DNS 名和 `*.` 子域后缀，后缀不包含根域。路径与已编码查询分别传入，不允许地址、端口或 URL 覆盖目标。只转发 Accept、Accept-Language、Content-Type、User-Agent；不转发 Cookie、Authorization 或内部元数据，也不读取系统代理环境。解析后直接连接经过检查的公网 IP，保留原域名的 TLS 验证与 SNI，不跟随重定向。

上游状态码（包括 429/5xx）原样返回，`ret_info` 只报告代理错误。响应支持 gzip、deflate、Brotli 解压，解压后最多 32 MiB；请求正文最多 1 MiB，查询与路径总计最多 16 KiB，头部最多 32 KiB。返回内容类型、缓存信息、Retry-After 与 Binance 用量头，去掉编码、长度、Cookie 和逐跳头。组件目录预留 48 MiB RPC 报文预算，以容纳 32 MiB 正文的 PB/JSON 包装。共享原生传输在启动前设置 64 MiB 帧上限，方法级目录限制仍然生效。单请求预算最多 60 秒，调用方取消可提前结束。

`ResolveDomains` 迁入 Trade 的解析实现：独立的精确域名白名单、最多 16 个域名、IPv4 公网地址过滤、TCP 探测、延迟排序、缓存与每域最多四个 IP。HTTP 白名单与 DNS 白名单独立配置。Trade 旧入口随 Collector 的调用方迁移在 E4 删除。

程序使用 `--config config/app.yaml --conf config/trpc_go.yaml`。loopback 11441 提供共享鉴权的 `/healthz`、`/readyz` 与 `/metrics`；启动需要 `MOOX_HEALTH_AUTH_ACCESS_KEY`、`MOOX_HEALTH_AUTH_SECRET_KEY`，部署设置 `MOOX_INSTANCE_ID`。配置严格拒绝未知字段、重复键与额外 YAML 文档。关闭时先撤下就绪状态，再停止 RPC 并释放 HTTP 空闲连接。

本机执行 `go test -race ./...` 与 `go vet ./...`。纯 Go Linux 目标在本机关闭 CGO 交叉编译；生产安装与调用方切换仍按[执行计划](../../docs/计划/网关与服务部署重构执行计划.md)后续阶段进行。
