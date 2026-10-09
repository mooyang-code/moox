# Console Proxy 内嵌 Caddy 执行计划

日期：2026-10-09

状态：待实施；本文生成不代表代码、构建、发布或部署已经完成。

源码基线：`d64c40757e3d962f970c36f9e9c62767c77d8c41`。执行前重新确认相关文件，保留并适配其他任务的改动。

## 1. 目标与已确定的决策

将 Caddy 作为 Go 库集成到 `moox-console-proxy`，由同一二进制、同一进程承担控制台 HTTPS 和前端反向代理。部署端不再安装、下载或管理独立的 Caddy 可执行文件。

本次讨论已经确定：

| 事项 | 决策 |
| --- | --- |
| 组件职责 | `console-proxy` 仅作为前端入口，不承载节点控制通信或服务间数据面 |
| 二进制 | `moox-console-proxy`，不另带 `bin/caddy` |
| 实现方式 | MooX 自有 main 调用 Caddy 库，不以 `caddycmd.Main()` 作为服务入口 |
| 进程模型 | Caddy 在 console-proxy 进程内运行，不通过 exec、子进程或系统 Caddy 启动 |
| HTTPS 与反向代理 | 使用 Caddy 标准模块；只允许为进程内排空增加最小生命周期适配，不重复实现证书管理和代理引擎 |
| 依赖 | 新模块加入 `go.work`，允许统一升级依赖；升级后执行工作区完整回归 |
| 静态资源 | 保留独立 `web-host`，本计划不把静态资源打进 console-proxy |
| 认证与业务 | 保留在 Admin；console-proxy 不接管登录、授权、数据库或 BFF 业务 |
| TLS 模式 | 保留现有 `internal`、`public` 两种能力，不把二进制集成变成证书体系重建 |
| 状态目录 | 保留实际证书、CA、ACME 账户和续期状态，不因组件更名而重新签发 |
| 安装来源 | 使用 MooX 自编译制品，取消官方 Caddy archive、checksum 清单和缓存变量 |
| 第一版配置更新 | 校验候选配置后受控重启；不新增远程热重载协议或 Caddy 管理 API |
| 项目原则 | 不保留双实现、旧命令兼容层或静默回退到官方 Caddy 的路径 |

“取消独立 Caddy”不等于把代理嵌入 Admin、Gateway 或其他业务进程。console-proxy 自身仍是独立的 MooX 组件。

## 2. 范围与非目标

### 2.1 本计划范围

1. 创建 console-proxy 模块并内嵌 Caddy。
2. 明确前端路由、TLS、生命周期、配置、日志和诊断契约。
3. 接入工作区、构建、完整安装包、binary-only 包和组件包。
4. 同步 shell 部署与 CLI 安装两条路径。
5. 更新组件注册、Doctor、现有健康检查和运维文档。
6. 在非前端替代入口验收后，删除旧 Caddy 安装、启动和回滚实现。
7. 完成独立审查、回归、构建与受控环境验证；正式发布另行执行交付门禁。

### 2.2 非目标

- 不合并 web-host、Admin、Gateway 进程。
- 不重写 Admin 认证、Gateway HMAC、ACL、防重放或路由快照协议。
- 不在 console-proxy 中实现 `/api/service/*` 或 `/api/gateway-control/*`。
- 不引入第三方 Caddy 插件，不开发业务型代理插件、自定义 ACME 客户端或通用反向代理配置平台；必要的排空适配见第 6.3 节。
- 不顺带修改 Monitor 的采集、存储、告警和调度实现；只同步新组件必需的身份与探针配置。
- 不自动信任本机系统证书库，不自动修改公网安全组或防火墙。
- 不把生成本计划视为实施代码、生产部署或删除线上进程的授权。

## 3. 当前实现与目标的差异

以下是本次检查的源码事实，不是对目标环境的在线验证。

| 范围 | 当前实现 | 目标 |
| --- | --- | --- |
| 代理组件 | 尚无 `modules/consoleproxy`；Doctor 仍使用 `admin_gateway` 描述公开入口 | 独立注册 `console_proxy` / `console-proxy` |
| 浏览器入口 | Caddy `9527` 转发 web-host `9528` 和 Admin `11000` | console-proxy 内嵌 Caddy，保持浏览器入口职责 |
| 控制通信 | `/api/gateway-control/*` 也经过浏览器 HTTPS 端口 | 迁到独立的节点控制入口，不经过 console-proxy |
| 服务通信 | Caddy `11001` 转发 Gateway loopback HTTP `11002` | 由经验证的服务入口承接，不经过 console-proxy |
| Gateway HTTP | 当前限制为 loopback，使用普通 HTTP server，未提供服务器 TLS 配置 | 非前端迁移工作先补齐替代能力，不能直接放开 loopback |
| 公开诊断 | 浏览器入口的 `/healthz`、`/readyz`、`/metrics` 返回 404 | 继续公开拒绝，另用 MooX 内部鉴权诊断入口 |
| Caddy 管理 | `caddy-managed.sh` 管理 `bin/caddy`、PID、2019 管理端口和官方 archive | MooX 管理 console-proxy 生命周期，不再调用旧 manager |
| 部署路径 | shell 与 Go CLI 内嵌安装脚本都调用旧 manager | 两条安装路径同步改造 |
| 工具链 | workspace 为 Go 1.25.0，CI 配置还有 Go 1.24 | 固定满足全部依赖要求的工具链 |

主要依据：

- [当前 Caddy 路由](../deploy/caddy/Caddyfile)。
- [Gateway 配置约束](../modules/gateway/internal/config/config.go) 与 [启动实现](../modules/gateway/internal/bootstrap/bootstrap.go)。
- [远程 HTTP 安全约束](../packages/gatewayauth/client.go)。
- [Caddy 管理脚本](../scripts/lib/caddy-managed.sh)。
- [shell 部署](../scripts/deploy/deploy-moox.sh) 与 [CLI 部署](../modules/cli/internal/setup/deploy/deploy.go)。
- [Doctor 组件目录](../packages/doctor/components.yaml)。

## 4. 目标架构与路由契约

```text
Browser HTTPS
      |
moox-console-proxy
  MooX config / lifecycle / diagnostics
  Embedded Caddy HTTP + TLS + reverse proxy
      |
      +-- /api/admin/* --> Admin loopback browser API
      +-- page/assets  --> web-host loopback HTTP

Node Gateway --> dedicated control endpoint --> Admin control handlers
Service / SCF --> dedicated service endpoint --> local Gateway
```

### 4.1 允许与拒绝的路由

| 请求 | 行为 |
| --- | --- |
| `/api/admin/*` | 转发 Admin，保留 path、query、method、body 和业务认证头 |
| 页面、前端静态资源、SPA 路由 | 转发 web-host，由 web-host 继续负责静态资源及 SPA fallback |
| `/api/gateway-control/*` | 明确返回 404，不转发 Admin，不落入静态资源 fallback |
| `/api/service/*` | 明确返回 404，不转发 Gateway |
| 其他未注册 `/api/*`，以及 `/api` | 明确返回 404，不能返回 index.html |
| `/healthz`、`/readyz`、`/metrics` | 公开入口返回 404 |
| Caddy 管理路由 | 不注册、不监听管理端口，不可从公开入口访问 |
| ACME HTTP challenge | public TLS 所需的协议入口，只处理证书挑战，不开放业务路由 |

明确拒绝的前缀要覆盖尾斜线、子路径和路径规范化后的访问。测试不能只检查一个字面 URL。

### 4.2 代理与安全语义

- 默认上游保留为同机 loopback：Admin `127.0.0.1:11000`、web-host `127.0.0.1:9528`。
- 第一版不支持任意远程上游、动态用户 URL 或 request 参数指定代理目标。
- 保留现有响应头、压缩策略和浏览器同源行为，不同时扩大 CSP 或跨域权限。
- 保留 HTTP/WebSocket/流式响应所需的转发语义；不读取或缓存完整业务 body，不替代上游认证。
- 不默认信任客户端 `X-Forwarded-*`。只有已核验的前置代理来源才能加入 trusted proxies。
- HMAC 等签名路径、query 和 body 不得被代理改写；使用实际登录和签名请求回归，不能只测匿名首页。
- HTTP/3 是否启用应保持现有有效配置；若启用，同时检查 UDP 监听归属和安全组要求，不只检查 TCP。

## 5. 非前端入口迁移门禁

这是彻底删除外置 Caddy 的前置依赖，不是让 console-proxy 临时兼任服务网关的理由。

### 5.1 必须交付的替代入口

| 旧通路 | 替代通路要求 | 必须迁移的调用方 |
| --- | --- | --- |
| `/api/gateway-control/routes`、`/status` | Admin 所属控制入口；具备节点可达性、服务身份验证和 control HMAC | 所有 Gateway 的 `control_plane.base_url`、CA 配置、部署生成配置 |
| `11001/api/service/*` | Gateway 所属服务入口；保持现有服务认证和远程加密要求 | Trade Secret、Strategy TradeOwner、Admin TradeConsole、远程 Collector、SCF |
| 服务地址发布 | 只发布新的真实服务入口，不再发布 console-proxy URL | SysDeploy、初始化种子、持久化服务地址、SCF 环境变量及安装模板 |

当前调用链的检查入口：

- `modules/gateway/internal/controlplane/client.go`：Pull/Report。
- `modules/trade/internal/secretclient/client.go`：Trade Secret。
- `modules/strategy/internal/bootstrap/logical_account_gateway.go`：Strategy TradeOwner。
- `modules/admin/internal/gateway/forward.go`：Admin TradeConsole。
- `modules/collector/internal/scfinvoker/client.go`：Collector 服务调用。
- `scripts/deploy/deploy-moox.sh`：`MOOX_SCF_SERVICE_GATEWAY_TARGET` 生成。
- `modules/admin/internal/service/sysdeploy/defaults.go`：服务入口发布。

### 5.2 尚需冻结的网络决策

本轮讨论没有决定 Admin/Gateway 替代入口的具体 TLS 实现、端口分配和证书签发所有者，不将这些内容冒写成已确认设计。

执行 P0 时必须先与 G7 或现行网络改造方案对齐，形成一张已确认的迁移表，逐项记录：旧地址、新地址、监听地址、协议、证书 SAN/CA、调用方、配置事实源、切换顺序、验证方法和负责人。

执行约束：

1. 不把 `11000`、`11002` 改为 `0.0.0.0` 明文监听来替代 HTTPS。
2. 不关闭客户端 CA/主机名校验，不使用 `InsecureSkipVerify` 绕过迁移。
3. 仅有私网 IP 不代表已经满足当前客户端的加密传输契约。
4. 不为替代入口新增外置 Caddy 进程，也不把它们重新塞回 console-proxy。
5. 同机 loopback HTTP 可继续使用；远程入口按冻结后的安全网络方案交付。
6. 替代入口尚未交付时，可以开发并测试新模块，但不能删除运行环境中的旧入口，也不能宣布本计划完成。

### 5.3 删除旧入口的证据

- 每个 Gateway 从新控制入口 Pull 到本次发布的新 route hash，并成功 Report。只看到旧缓存可用不算通过。
- Trade Secret、Strategy TradeOwner、Admin TradeConsole 和远程 Collector 实际成功调用。
- SCF 已发布函数的真实环境配置指向新入口，至少一次真实调用成功；源码模板修改不等于已迁移。
- 错误 CA、错误服务身份、缺失或错误 HMAC 均被拒绝。
- 新入口正常工作时停止旧 Caddy，重做以上验证；调用不能暗中依赖旧入口。

## 6. 模块、配置与进程契约

### 6.1 模块布局

新增内容按现有 Go 模块习惯组织，不建立新的共享代理框架：

```text
modules/consoleproxy/
  go.mod
  go.sum
  cmd/server/main.go
  config/app.yaml
  internal/config/
  internal/bootstrap/
  internal/engine/
  internal/health/
  README.md
```

`internal/engine` 只封装本组件对 Caddy 的配置加载、启动和停止，不对外暴露通用 Caddy SDK。具体文件拆分由实现规模决定，避免空壳分层。

统一身份：模块目录 `consoleproxy`，组件 ID `console_proxy`，服务名 `console-proxy`，二进制 `moox-console-proxy`。注册时更新所有对应关系，不把 Admin 的健康状态当作代理进程状态。

### 6.2 配置真值

运行配置以组件 `app.yaml` 为唯一事实源，部署脚本负责生成它。第一版字段至少包含：

| 配置组 | 内容与约束 |
| --- | --- |
| public | 主机名/IP、绑定地址、浏览器 HTTPS 端口；保持现有默认 9527 和显式覆盖能力 |
| tls | `internal` / `public`、证书持久化目录、public ACME 所需设置 |
| upstreams | 同机 Admin、web-host 地址，校验为允许的 loopback 目标 |
| health | 独立 loopback 诊断地址；P0 在统一端口清单中分配，不复用 Caddy 2019 |
| lifecycle | 停机排空时间、启动及 TLS 就绪等待上限 |
| logging | 复用 MooX 日志输出和轮转配置，敏感字段不进入日志 |

如复用 Caddyfile，将其作为代码仓库维护的固定模板，由类型化配置渲染后经 Caddy adapter 转为 JSON。不要同时允许任意外部 Caddyfile 覆盖组件路由边界；渲染使用可靠的转义或结构化构造，禁止直接拼接用户字符串。

生成配置必须显式关闭 Caddy admin endpoint、动态配置拉取和 autosave/resume，防止出现第二份配置真值。不依赖默认环境变量改变监听和存储位置。

### 6.3 启动、运行与退出

1. 加载并校验 MooX 配置，初始化统一日志和内部诊断；ready 初始为 false。
2. 确认数据目录权限、监听配置和必要的文件能力，拒绝接管无关进程的端口。
3. 构造受约束的 Caddy 配置，注册标准模块与 `time/tzdata`。
4. 使用 `caddy.Load` 或 `caddy.Run` 启动同一进程内的 HTTP/TLS 应用。调用返回后 main 仍等待退出信号，不能直接结束。
5. internal 模式完成第 7.1 节的 CA 发布，再用实际 HTTPS 握手确认对应 SNI/Host 的证书可用。public ACME 尚未完成时保持 not-ready，不把 Load 成功当作证书已签发。
6. SIGTERM/SIGINT 到来后先标记 not-ready，关闭业务请求准入并执行有界排空，再停止 Caddy，最后清理证书锁、诊断服务和日志资源。
7. 启动失败退出非零；可恢复的上游业务故障不导致进程自杀或重启循环。

Caddy 使用进程级状态，应用中只建立一个 engine 所有者；集成测试不得并行启动多个相互覆盖的 Caddy 配置。

不调用 `caddycmd.Main()` 后，不能假定 CLI 的初始化和退出处理仍会执行。实现时显式核对 ACME 条款确认、User-Agent、信号处理及 CertMagic 锁清理；不得为了复用 CLI 初始化而让 CLI 接管整个 MooX 进程。

**排空实现门禁：** v2.11.4 的 HTTP App 仅在 `caddy.Exiting()` 为 true 时等待所有 shutdown goroutine；普通库调用 `caddy.Stop()` 不自动进入该状态。因此不能把 Stop 返回当作旧请求已完成，也不能用固定 sleep 或访问未导出的退出标志代替排空。

P2 先完成真实请求原型，再固定以下最小适配：在前端业务路由最外层注册本组件自有的准入/活动请求计数 handler，所有实际代理仍使用标准模块。退出时关闭准入，新业务请求返回 503，等待已有 HTTP/流式/WS 请求结束；到配置 deadline 后取消剩余工作，停止引擎并退出，保证遗留连接最终关闭。排空计数必须覆盖 WS/流式请求的完整存续期，不能只统计 response header 或升级握手。

配置非零且有上限的 Caddy `grace_period`，但它不能替代上述应用级等待。使用真实长请求证明 deadline 内可完成、超过 deadline 能结束；清锁、释放依赖和 main 退出必须晚于正常排空完成或明确的超时强制结束。若公开 API/适配原型无法满足这一契约，P2 不通过，先调整生命周期设计，不通过 fork Caddy 或反射私有字段绕过。

### 6.4 命令与配置更新

第一版提供以下 MooX 命令契约，具体参数排版沿用现有组件模式：

- `serve --config <path>`：运行代理。
- `validate --config <path>`：解析配置、生成目标配置并检查组件不变量；不监听业务端口，不签发证书，不改生产状态。
- `version`：输出 MooX 版本、commit、构建时间和 Caddy 引擎版本，不能只输出 Caddy 版本。

`validate` 不等于完整 module provisioning，也不等于 TLS 就绪。需要执行 `caddy.Validate` 的测试使用隔离的测试状态目录，避免 provisioning 对生产 CA/账户产生写入。

第一版配置变更通过 validate 后受控重启应用，不保留 `caddy reload`、`caddy upgrade`、2019 管理 API 或双重热重载协议。Caddy 在内部自动续期证书不需要重启组件。若未来需要无中断热重载，单独扩展 MooX 统一配置生命周期。

### 6.5 健康、诊断和日志

- 使用 `packages/healthz` 的现有 payload、liveness/readiness 区分和鉴权包装，不新增匿名诊断端点。
- liveness 表示进程及引擎仍在工作；上游 Admin/web-host 临时失败不应被解释为必须重启代理。
- readiness 表示已加载预期配置、代理入口可用、对应 TLS 握手可用且不在排空阶段。
- Admin/web-host 的业务可达性作为依赖状态单独报告，部署端到端验收仍必须检查它们。
- 公开入口继续拒绝 health/metrics；内部诊断只绑定 loopback，Monitor/Doctor 沿用已有受保护的采集通路，不为远程采集放开匿名监听。
- 记录组件版本、Caddy 版本、有效监听、TLS 模式、证书过期时间和加载失败原因；不记录 token、HMAC secret、Cookie、私钥或完整敏感 body/query。

## 7. 证书、状态与部署契约

### 7.1 持久状态

第一版保留现有 `data/caddy`、`certs/caddy` 的状态归属和运维路径，避免仅因二进制更名引入数据目录迁移。组件配置显式指向实际 Caddy storage root；执行前核对旧 XDG 路径下的真实存储层级，不能把上层目录误当成 root。

- 保留 internal CA 的完整链和私钥、public ACME 账户、证书、锁及续期状态。
- 普通重新部署、版本回滚和业务 `reset-data` 都不删除证书状态。
- 复制状态目录前停止其唯一写入者；新旧代理不能同时写同一份生产存储。
- 权限沿用最小权限原则；制品包不携带现场私钥、账户、secrets 或 data。
- CA 导出和明确授权的信任库安装工具继续保留；不机械删除所有文件名含 caddy 的工具。
- internal CA 指纹在切换前后保持一致，除非另有明确的证书轮换操作。

**显式接替旧 `publish_ca`：** internal 首次启动等待实际 root 创建，解析并验证 CA 属性，计算 SHA256 指纹，与已有发布指纹比较后原子发布 `certs/caddy/root.crt`、`root.sha256`。不匹配时失败并要求显式轮换，不覆盖旧的信任材料。首次安装没有旧指纹时建立基线，后续重部署和回滚检查该基线。

public 模式成功激活时清理过期的 internal CA 发布副本及其指纹，避免工具把 public 入口误判为 internal；不删除持久 storage 中供回滚使用的 CA/账户。失败回滚恢复对应模式的发布副本和指纹。启动就绪、CA 导出和信任库工具都必须消费这份经过验证的发布结果，而不是任意扫描一个 root 文件。

### 7.2 权限与监听

保留非 root 运行方式。Linux 下如需要 80/443 等特权端口，把所需最小文件能力施加到 `moox-console-proxy`，不再施加到已删除的 `bin/caddy`；每次替换二进制后重新核对能力。

public 模式保留当前 ACME challenge 策略及端口需求。不能因为浏览器端口是 9527 就假定不需要 80，也不能为解决挑战失败自动抢占其他服务端口或修改防火墙。

### 7.3 发布制品

完整包、binary-only 包和组件包都必须包含新二进制及所需配置/生命周期脚本。把新制品纳入现有 MooX SHA256 清单，不新增一套 Caddy 专用成品签名体系。

`go.sum` 负责依赖源码完整性；发布包和二进制继续走 MooX 制品校验。官方 archive SHA512 清单随 archive 消费路径一起删除。

保持现有构建矩阵：Linux amd64/arm64、Darwin amd64/arm64、Windows amd64。Linux 两架构必须完成真实运行验证；其他平台至少完成编译和打包校验，不将编译成功描述为已支持对应平台生产部署。

### 7.4 激活与回滚

1. 预先验证非前端迁移门禁和候选制品、配置；候选失败时不停止旧服务。
2. 保存当前版本、配置、证书状态位置、权限及运行身份的回滚信息。
3. 停止唯一的旧代理写入者，完成一致的状态保留/复制，再激活新版本。
4. 启动 console-proxy，验证内部鉴权诊断、TLS 握手、页面及已认证 API。
5. 失败则恢复完整上一版制品与配置、权限和服务地址，停止失败的新进程后再启动旧版本。
6. 首次从旧架构切换时，把 Admin/Gateway 地址迁移纳入同一发布批次的回滚清单；只回滚代理二进制不一定能恢复旧通信链路。

回滚使用归档的完整上一版制品，不为旧 manager 保留永久运行兼容分支，也不在失败时临时联网下载 Caddy。第一版受控重启有短暂入口不可用窗口，发布前明确维护窗口，不宣称零停机切换。

P4/P5 只交付候选包、部署实现和隔离环境测试，不激活当前运行环境。P6 完成非前端替代入口准备与迁移后，在受控目标演练整批切换。若替代入口与旧 Caddy 使用同一端口、无法并行验证，P0 必须明确同批停旧/启新及全链路恢复顺序，不能要求冲突的两个监听同时成功。正式环境切换须在 P7 独立审查、完整回归及发布放行后，按同一已验证顺序执行。旧完整制品与其生命周期在实际切换前保持不变。

## 8. 分阶段执行清单

阶段之间必须满足退出条件。这里的 G2/G7 使用本次讨论中的“安装包重组”“Caddy 仅在 control”含义，不代表当前仓库已存在同名任务文档。

### P0：冻结边界与依赖门禁

- [ ] 重新核对源码基线、并行改动和当前运行拓扑。
- [ ] 冻结第 5 节入口迁移表，确认 Admin/Gateway 替代入口的方案及交付任务。
- [ ] 冻结组件 ID、服务名、诊断端口、配置路径及实际 storage root。
- [ ] 记录现有 internal CA 指纹、public 证书身份和 ACME 状态位置，不输出私钥或凭据。
- [ ] 明确第一版采用受控重启，不依赖 Caddy admin endpoint。

退出条件：实现输入无歧义；没有把仍被节点/SCF 使用的旧入口误列为可直接删除。

### P1：工作区、工具链与最小模块

- [ ] 新增 `modules/consoleproxy` 的 go.mod、go.sum、main、配置加载和版本输出。
- [ ] 初始固定 Caddy `v2.11.4`，加入标准模块和 tzdata。
- [ ] 加入 go.work，统一解析依赖图，审查 zap、Prometheus、x/net、x/crypto 等实际升级。
- [ ] Caddy 该 tag 自身最低 Go 为 1.25.1；选定满足完整依赖图的具体工具链版本并统一到 CI、CNB、开发与发布构建。
- [ ] 不只写“Go 1.25”，也不依赖 `GOTOOLCHAIN=auto` 在发布时临时下载未预置工具链。
- [ ] 新模块纳入现有 workspace test/vet/格式与边界检查，保留其他组件的 CGO 设置。

退出条件：版本输出和配置测试通过；依赖升级有完整清单；新模块没有被全量测试静默跳过。

### P2：进程内引擎与前端路由

- [ ] 实现受约束配置渲染/适配，关闭 Caddy admin、autosave 和动态配置拉取。
- [ ] 实现同进程 Load/Run、main 等待、启动失败清理和退出排空。
- [ ] 先通过第 6.3 节排空原型门禁，再实现准入/完整活动请求跟踪，禁止用 Stop 返回或 sleep 充当完成信号。
- [ ] 实现 internal/public TLS，显式指定持久存储，不改变现有证书身份。
- [ ] 接替 CA 发布副本、指纹防轮换和 public 模式陈旧副本清理行为。
- [ ] 实现前端允许路由和非前端拒绝路由；保留 headers、压缩、WS/流式请求行为。
- [ ] 实现无生产副作用的 validate 命令，测试独立引擎版本输出。
- [ ] 使用测试证书/受控 ACME 测试环境验证，不让普通单元测试访问生产 CA。

退出条件：不依赖 PATH 中的 caddy，也没有 Caddy 子进程；实际 HTTPS、页面、API 转发及真实长请求排空通过；internal 首装能取得正确的 CA 发布副本。

### P3：组件诊断与注册

- [ ] 接入 healthz 共享状态与鉴权，建立独立内部诊断监听。
- [ ] 分开 liveness、readiness 和上游依赖故障；TLS 未就绪不得报 ready。
- [ ] 更新 Doctor 组件目录、PID/二进制身份映射、部署注册及必要显示名。
- [ ] 保持 Admin、web-host、console-proxy 三种进程身份分离，不创建重复的虚假入口组件。
- [ ] 更新 Monitor 所需的组件发现/探针配置；不扩展 Monitor 核心改造。

退出条件：内部合法鉴权可观测，匿名被拒绝；公开诊断不可达；代理故障能定位到新组件。

### P4：构建与安装包重组，对应 G2

- [ ] `build.sh` 新增 `console-proxy` target 并加入 `all`；只对本组件使用 CGO_ENABLED=0。
- [ ] 完整 release 创建组件目录，装入新二进制、配置和生命周期脚本。
- [ ] binary-only release 的显式 binary_names 和 SHA256 manifest 加入新二进制。
- [ ] binary-only publish/restart 识别新服务；确认所选新制品有对应摘要并经过校验。
- [ ] 组件 ZIP/单服务安装路径接入 console-proxy，包内不含 data/run/logs/secrets/certs。
- [ ] 五平台矩阵都检查制品内容，避免某条打包路径仍夹带旧 archive 或遗漏新组件。

退出条件：三类候选制品内容正确，构建不下载官方 Caddy archive，制品摘要包含新二进制。候选包不得在 P6 门禁之前替换当前运行版本。

### P5：双部署路径实现与隔离测试

- [ ] 改造 shell 部署生成的 start/stop/status/healthcheck、stage、activation 和 rollback。
- [ ] 改造 CLI 的控制节点安装内嵌脚本，包括停止写入者、状态复制、启动失败恢复和 browser readiness。
- [ ] 更新端口归属、PID 身份、文件能力及失效 PID 处理，不误杀其他 Caddy 或其他服务。
- [ ] 候选版本的代理自愈只重启 moox-console-proxy，不再调用 `caddy-managed.sh`；不提前修改当前运行版本的生命周期。
- [ ] `reset-data` 和版本目录切换保留证书/ACME 状态；回滚不生成新的 CA。
- [ ] 覆盖 internal 首装、internal/public 模式切换、指纹异常拒绝和失败恢复发布副本。
- [ ] 更新控制节点 profile；非 control profile 不打包、不启动 console-proxy。

退出条件：shell 和 CLI 均能在具有替代网络入口测试条件的隔离环境首次安装、重复安装、失败回滚；状态与权限正确。这里不包含现网激活，当前运行环境继续使用完整旧版本，直到 P6 切换。

### P6：非前端迁移、切换演练与旧实现删除，对应 G7

按以下顺序执行，不将代码完成误当作入口已切换：

- [ ] P6-A：完成第 5 节替代入口、配置种子与持久化地址迁移准备，保存完整旧制品及网络配置回滚信息。
- [ ] P6-A：可并行的新入口先完成所有调用方迁移和真实验证；同端口替换场景先完成维护窗口、同批激活及失败恢复演练。
- [ ] P6-B：在获授权的受控目标执行第 7.4 节整批切换，停止旧 Caddy，激活所需的 Admin/Gateway 替代入口和 console-proxy，完成冲突端口与状态写入者的交接。
- [ ] P6-B：重新验证 Gateway 新 route hash/Report、所有服务调用及 SCF 真实调用，再验证浏览器登录和已认证 API；任何一项失败则按整批策略回滚。
- [ ] P6-B：shell/CLI 在受控目标的首次/重复安装与回滚验收完成；确认旧 9527/11001 通路不再被暗中依赖。
- [ ] P6-C：受控切换验证通过后完成旧实现清理，重建最终候选制品并复测；P7 放行前不正式发布，不保留永久双实现。
- [ ] 删除旧 manager、官方 checksum 文件、archive 下载与缓存变量、旧 2019 探针和 dead code。
- [ ] 删除或替换旧 Skill prerequisite；保留仍有调用方的 CA 导出/信任库工具。
- [ ] 更新所有现行测试，移除对旧 helper/archive 的正向依赖，增加旧依赖不存在的反向断言。
- [ ] 更新架构、运维和 Skill 文档。历史计划如仍保留，标明被本计划替代的范围，不把历史描述当作当前命令。

退出条件：新架构运行不需要旧 Caddy 二进制、脚本或网络下载；非 control 节点没有 console-proxy/Caddy 进程。

### P7：独立审查与交付

- [ ] 新起 codeCR Agent，审查路由边界、TLS/证书状态、生命周期、两条部署路径和依赖升级回归。
- [ ] 主 Agent 独立核验所有审查结论并处理有效问题；所有审查 Agent 完成后再汇总结论。
- [ ] 在干净集成工作树完成第 10 节验证；不能把依赖污染、漏测或未验证的平台标为通过。
- [ ] 保存构建清单、测试结果和受控环境的进程/监听/证书/请求证据。
- [ ] 按当前任务授权完成提交、推送及发布；正式环境操作前确认目标、版本、维护窗口和回滚制品。
- [ ] 正式环境获授权后复用 P6-B 已验证的整批切换与回滚顺序，完成第 11 节证据验收。

退出条件：代码、独立审查、回归、构建、受控部署分别有证据；需要正式交付时再满足生产门禁。

依赖关系：`P0 -> P1 -> P2 -> P3/P4 -> P5 -> P6-B -> P6-C -> P7`；非前端网络工作及 P6-A 可与 P1-P5 并行，但 P6-B 同时依赖 P5 和 P6-A。旧入口删除还必须等待 P6-B 全链路验收；不得用临时放宽 console-proxy 路由规避等待。

## 9. 实施文件清单

下表为修改入口，不要求为了形式修改每一个文件；执行时沿真实调用链更新，删除无调用的旧分支。

| 范围 | 文件/目录 | 动作 |
| --- | --- | --- |
| 新模块 | `modules/consoleproxy/` | 新增引擎、配置、生命周期、诊断与测试 |
| 依赖 | `go.work`、实际升级模块的 go.mod/go.sum | 工作区集成与依赖统一 |
| 工具链 | `.github/workflows/ci.yml`、`release.yml`、`.cnb.yml` | 统一具体 Go 工具链并接入验证 |
| 构建 | `scripts/build/build.sh`、`Makefile` | 新目标与 test-console-proxy 门禁 |
| 完整发布 | `scripts/release/release.sh`、`release-matrix.sh` | 新组件及平台矩阵 |
| binary-only | `scripts/build/build-release-binaries.sh`、`scripts/release/publish-release-binaries.sh` | 制品列表、摘要和重启 |
| 组件包 | `scripts/build/package-service.sh`、`scripts/test/contract/test-package-service.sh` | 标准组件包结构 |
| shell 部署 | `scripts/deploy/deploy-moox.sh` | stage、配置、启动、自愈、激活及回滚 |
| CLI 安装 | `modules/cli/internal/setup/deploy/deploy.go`、`deploy_test.go` | 同步 Go 内嵌安装流程 |
| 身份与注册 | `packages/doctor/components.yaml`、`modules/cli/internal/doctor/bootstrap.go`、`config/setup/service-deployments.yaml` | 独立代理身份与内部健康地址 |
| 服务配置 | `modules/admin/internal/service/sysdeploy/defaults.go`、`modules/cli/internal/setup/client/registry.go` | 入口语义和服务地址 |
| 展示 | `modules/monitor/internal/healthview/builder.go` | 仅必要的新组件显示适配 |
| 旧实现 | `scripts/lib/caddy-managed.sh`、`scripts/deps/caddy-v2.11.4-checksums.txt` | P6 门禁通过后删除 |
| 配置模板 | `deploy/caddy/` | 迁移前端模板；非前端模板在入口迁移后退出 |
| CA 工具 | `scripts/deploy/install-caddy-ca.sh`、`skills/moox/scripts/caddy-ca.sh` | 保留或调整实际 CA 路径，不与下载代码一起删除 |
| Skill prerequisite | `skills/moox/scripts/caddy-prerequisite.sh` 及对应测试 | 删除独立 Caddy 安装前置条件 |
| 运维文档 | `docs/运维/管理台HTTPS与证书.md`、`docs/大仓架构.md`、`docs/节点服务网关架构.md`、`docs/架构总览.md` | 更新边界和命令 |
| Skill 文档 | `skills/moox/SKILL.md`、`skills/moox/references/` 下的 HTTPS/release/setup 文档 | 更新安装、升级、证书和恢复流程 |

非前端替代入口的具体文件清单由 P0 网络迁移表冻结；不要在执行 console-proxy 模块任务时未经确认扩展为全仓网络重构。

## 10. 验证矩阵与命令

### 10.1 必测场景

| 类别 | 场景 | 通过条件 |
| --- | --- | --- |
| 单进程 | PATH 无 caddy，禁止外部 archive 下载 | 代理能独立运行，无 Caddy 子进程 |
| 配置 | 非法端口、非法上游、缺失状态目录权限 | 明确失败，启动失败非零，不改生产状态 |
| TLS internal | 合法 CA、错误 CA、错误 SAN/SNI | 合法信任成功，错误信任或主机身份失败 |
| TLS public | 测试 ACME 签发/续期、启动等待、状态恢复 | 正确就绪，续期无需外部进程，重启保留账户 |
| 路由 | 首页、hash 静态资源、SPA 页面 | 内容及缓存语义正确 |
| API | 登录、带 session HMAC 的已认证请求 | 上游认证正常，body/path/query 不被改写 |
| 非前端拒绝 | gateway-control、service、未知 api、路径变体 | 明确 404，未进入 Admin/Gateway/静态 fallback |
| 代理协议 | WS、流式响应、上游断连 | 升级/透传正确，无重复重试导致业务副作用 |
| 诊断 | 内部合法签名、缺失/过期/重放签名、公开 URL | 内部按现有鉴权契约处理，公开诊断 404 |
| 启停 | 启动端口冲突、SIGTERM、真实长 HTTP/流式/WS 请求、重复启停 | 新业务请求拒绝，deadline 内存量请求完成，到期遗留连接结束；不抢占无关进程，无遗留监听或锁 |
| 配置变更 | 候选配置无效、新版本启动失败 | validate 不停旧服务；激活失败可恢复上一版 |
| 状态 | 首装、重部署、reset-data、模式切换、版本回滚、CA 指纹异常 | 正确发布或清理 CA 副本，异常轮换被拒绝，CA 指纹及 ACME 状态不丢失 |
| 权限 | 特权端口、新二进制替换、无需特权时 | 新二进制能力正确且最小化 |
| 安装 | shell 与 CLI 两种路径、control/非control | 两条安装链一致；非control不装代理 |
| 打包 | 完整包、binary-only、单服务包、五平台 | 新制品齐全、摘要正确、无现场私密状态 |
| 网络迁移 | Pull 新 hash/Report、Trade/Strategy/Admin/Collector/SCF | 停旧 Caddy 后真实调用仍成功 |
| 依赖升级 | 整个 Go workspace 及受影响前端/部署契约 | 无共享依赖回归，失败有归因和修复 |

### 10.2 当前已有的验证入口

以下命令来自当前 Makefile，不在生成本文时执行：

```bash
make test-go
make test-release
make verify-custom-setup
make test-script-contracts
make test-skill-contracts
make test-docs-architecture
make verify
```

当前 `make test-caddy` 还绑定旧 manager 测试。实施中用新的 `make test-console-proxy` 替换其运行时契约，并保留仍有效的 CA 工具测试；不能简单删除旧测试而不补新行为覆盖。

`make verify` 的 `proto-check` 要求干净工作树；在已提交改动的干净集成树执行，不清理或覆盖用户的未提交文件。新模块加入 go.work 后检查 workspace 测试脚本确实枚举到它。

### 10.3 计划新增的验证入口

以下为待实现接口，当前目录/target 尚不存在：

```bash
cd modules/consoleproxy
go test -count=1 ./...
go vet ./...
```

回到仓库根目录：

```bash
make test-console-proxy
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh console-proxy
TARGET_GOOS=linux TARGET_GOARCH=arm64 ./scripts/build/build.sh console-proxy
```

`test-console-proxy` 应聚合配置、TLS/路由集成、生命周期和双部署路径契约，并接入 `make verify` 与 CI。race 测试在支持的本机工具链下单独运行，不把 `CGO_ENABLED=0` 的交叉编译环境误用于要求 race runtime 的测试。

测试 public ACME 使用受控测试服务或测试 CA；正式 CA 签发只属于获授权后的环境验收。普通测试和打包契约不得依赖 GitHub release archive 下载。

## 11. 发布与正式环境验收

本节是未来实施后的交付门禁，不是本次文档任务的操作清单。

发布前确认目标主机、节点角色、版本/commit、制品摘要、证书状态备份、维护窗口及完整回滚制品。先验证受控环境，再进行正式环境切换，不把本机测试等同于部署完成。

正式环境至少保存以下脱敏证据：

1. control 上运行的是 `moox-console-proxy`，有效 HTTPS 监听归该进程，未启动外置 Caddy。
2. 非 control 节点不运行 console-proxy/Caddy，非前端入口由已确认的网络方案承载。
3. 使用正常 CA/系统信任完成 HTTPS 校验，internal CA 指纹与切换前一致；不以 `curl -k` 证明信任正确。
4. 浏览器可加载实际页面及静态资源，登录和已认证 API 正常；公开非前端/诊断路由拒绝。
5. 每个 Gateway 获取新 route hash 并 Report，业务服务及真实 SCF 调用正常。
6. 停机/重启、自愈、证书状态保留和可执行回滚步骤通过；public 续期有受控测试证据和正式状态观测。
7. 安装和重启不发起 GitHub Caddy archive 下载；没有运行时回退安装分支。

若无法取得生产或云端访问条件，保留本地与受控环境结果，并把正式环境门禁标为未完成，不据此宣布整项交付完成。

## 12. 完成定义

- [ ] console-proxy 是前端专用入口，Caddy 是其进程内引擎。
- [ ] 只有 `moox-console-proxy` 代理制品，没有外置 Caddy 安装/启动依赖。
- [ ] Admin、web-host、Gateway 的职责保持独立，非前端替代入口及调用方已验收。
- [ ] 配置只有一个事实源，Caddy 管理 API 和 autosave/resume 未形成旁路。
- [ ] 证书、CA、ACME 状态、最小权限和进程身份在安装、重部署与回滚中正确。
- [ ] shell/CLI、三类包、五平台构建与相关注册/运维入口均覆盖。
- [ ] 工作区依赖升级完成全量回归，独立 codeCR 审查的问题已处理。
- [ ] 受控部署和按授权要求的正式环境验证分别完成并留有证据。
- [ ] 当前生效的旧 manager、archive/checksum/cache 调用和文档指令已退出，无双实现。

## 附录：上游依据

- [Caddy v2.11.4 的 Go 版本及依赖](https://github.com/caddyserver/caddy/blob/v2.11.4/go.mod)：该 tag 自身声明 `go 1.25.1`，最终工具链还须满足完整依赖图。
- [Caddy Run/Load/Stop](https://github.com/caddyserver/caddy/blob/v2.11.4/caddy.go)：支持 Go 进程内加载配置和应用生命周期管理。
- [Caddy AdminConfig/ConfigSettings](https://github.com/caddyserver/caddy/blob/v2.11.4/admin.go)：管理端点与配置持久化必须显式控制。
- [Caddy CLI 初始化](https://github.com/caddyserver/caddy/blob/v2.11.4/cmd/main.go)：内嵌模式不能假定 CLI 的 ACME 初始化仍自动执行。
- [Caddy HTTP 启停实现](https://github.com/caddyserver/caddy/blob/v2.11.4/modules/caddyhttp/app.go)：停机排空和配置生效要通过实际生命周期测试确认。
- [Caddy 官方 main](https://github.com/caddyserver/caddy/blob/v2.11.4/cmd/caddy/main.go)：标准模块及 tzdata 的引入方式。

上游约束按固定 tag 核对，不能把最新 Caddy 文档的工具链要求直接套到 v2.11.4，也不能仅固定 Caddy tag 而忽略共享工作区实际选中的传递依赖。
