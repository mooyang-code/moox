# 首次部署

新建一套 MooX 时按本文执行。用户必须在部署前填写 `moox.toml`，它是初始化的输入，不是部署产物。
设计背景见 `docs/部署与运维.md` 和 `docs/总体设计.md`。

## 用户准备

先给用户看 `moox.toml.example`。用户把它填成仓库根目录的 `moox.toml`，并在 Agent 继续之前保护好：

```bash
chmod 0600 ./moox.toml
```

用户必须在部署前填写 `moox.toml`。该文件包含明文凭据，初始化完成后仍由用户保管；CLI 对文件保持不变，
不删除、不改写。

### 文件结构

| 段 | 内容 |
| --- | --- |
| `[admin]` | 管理台的初始用户名和密码 |
| `[tencent_cloud]` | 腾讯云的 `secret_id`、`secret_key` 和默认地域（用于防火墙、SCF、主机网络发现） |
| `[eventbus]` | 消息总线的端口和是否启用 TLS；地址取自部署了 `eventbus` 的主机 |
| `[paths]` | `deploy_root`：每台主机的部署根目录默认是 `<deploy_root>/<主机 ID>` |
| `[notification]` | 告警推送渠道和 webhook，唯一需要用户提前填写的监控专用信息 |
| `[hosts.<主机 ID>]` | 一台 MooX 主机：公网地址 `address`、SSH 信息，可选的 `private_address`、`region`、`provider`、`root`、`tls_mode` |
| `[placements]` | 部署表：每台主机上运行哪些业务组件 |
| `[compile_host]` | 编译主机（可选），只用于构建需要 CGO 的 Linux 二进制，不是 MooX 主机 |
| `[egress_proxy]`、`[egress_proxy.dns]` | 经出口代理访问的域名，以及出口代理为 SCF 解析的域名 |
| `[storage_retention]`、`[storage_view]`、`[collector_retention]` | 数据保留期和 View 维护参数 |
| `[factors]`、`[scf_fetcher]` | 默认因子和 SCF 采集函数的发布参数 |

`[hosts.<主机 ID>]` 里 `control` 是固定的主机 ID，运行控制台和管理后台，必须存在。其他主机的 ID 自定义，例如
`storage`、`compute-1`。`address` 是公网地址：SSH、跨主机入口和控制台都用它。`private_address` 与 `region`
只有一个用途：让 SCF 就近访问部署在该主机上的外部接入，没有部署外部接入的主机不需要填写。腾讯云主机把 `provider`
设为 `tencent`，防火墙和 SCF 路由才会处理它。

`[placements]` 只写业务组件，主机网关和主机采集器每台主机自动部署：

```toml
[placements]
control   = ["console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode",
             "factor-mgr", "strategy"]
storage   = ["storage-primary", "storage-node", "storage-view", "access"]
compute-1 = ["trade", "access", "egress-proxy"]
```

组件 ID 和部署范围由代码里的组件目录决定（`packages/servicecatalog`）：`control` 范围的组件只能在 control 上，
`single` 副本的组件在所有主机中最多出现一次，受保护的组件（`admin`、`console-proxy`、`web-host`）不能从 control
的列表里去掉。校验不通过时 `setup validate` 和部署命令会整体拒绝。

`eventbus` 只包含公网连接事实：SCF 可访问的地址、监听端口和必须开启的 TLS。用户不填写消息总线的账号、token、CA
或私钥，MooX 在部署时生成这些材料。`notification.webhook_url` 填写企业微信或飞书机器人的 HTTPS webhook；留空不会
关闭监控采集、查询和规则计算，只是不发送站外告警。

不要在 `moox.toml` 里重复登记组件的端口、健康检查地址、指标 subject 或实时 Dataset + Frequency：端口和健康检查在
组件目录里；所有启用中的实时 TimeSeries Dataset + Frequency 由 Monitor 从 Collector 规则和 Factor 因子集自动刷新。

`[compile_host]` 只用于构建 Storage 等需要 CGO 的 Linux 二进制（`scripts/build/build-storage-linux.sh`，部署命令会自动
调用），连接时按 MooX 已核验的主机指纹库校验。它不是 MooX 主机，除非同时写进 `[hosts]`，否则不会被监控。

## 阶段一：校验、防火墙与首次部署

Agent 只可以检查文件是否存在，然后按顺序调用：

```bash
test -e ./moox.toml || exit 2
./bin/moox-cli setup validate --file ./moox.toml
./bin/moox-cli setup firewall --file ./moox.toml
./bin/moox-cli setup bootstrap --file ./moox.toml
./bin/moox-cli setup apply --file ./moox.toml
./bin/moox-cli setup status --file ./moox.toml
```

如果 `validate` 报告 `host_key_unknown`，通过独立可信的渠道取得每台主机的 SHA256 指纹，执行
`setup trust-host --host <主机 ID> --fingerprint '<指纹>'`（编译主机的 ID 是 `compile`），然后重新校验。不要仅因为
同一条 SSH 连接自己报告了指纹就接受它。

`setup firewall` 按部署表同步腾讯云主机的入站规则（控制台 9527、主机之间的 11003、外部接入 11004、消息总线端口、
非 control 主机的健康端口只对 control 开放），必须在 `bootstrap` 之前执行：其他主机的主机网关要通过 control 的
11003 取到第一份快照才算就绪。

`setup bootstrap` 完成空环境的首次部署：把 control 的发布装上并离线初始化管理后台（建表、生成 MooX 私有 CA、
写入全部主机和部署、生成调用方密钥、为 control 签发主机网关证书），启动 control，取回 moox-cli 的签名密钥，
再依次部署其他主机。可以重复执行，已有的表、CA 和密钥都会复用。

控制台代理使用 internal 证书时，`setup apply` 会在登录探测之前检查并安装本机浏览器对根证书的信任（需要提权时
CLI 会直接提示管理员授权）；中断后可以单独执行 `./bin/moox-cli setup trust-browser --file ./moox.toml`。公网 IP/DNS
自动使用操作系统信任的 Let's Encrypt 证书，不需要安装根证书。

`apply` 只有在 JSON 里 `login_api: valid` 时才算成功。在 `status` 返回 `completed` 之前，不要询问 Storage 或业务元数据。
本阶段结束时，部署表里的全部组件已经就绪，初始用户和主机记录已写入，公网登录接口有效。

## 阶段二：默认元数据初始化

Storage 就绪之后，初始化 A 股、加密货币等默认空间、它们的 Dataset 与 Field 契约，以及内部监控元数据：

```bash
./bin/moox-cli setup init --file ./moox.toml --config-dir ./config/setup
```

只有 `setup init` 可以用 `moox.toml` 经 SSH 隧道访问 Admin 和 Storage 主机。它创建或核对 Admin 业务空间和全部 Storage
元数据，激活就绪的 Dataset 并验证结果。`--storage-host` 默认取部署表里部署了 `storage-primary` 的主机。不要在 Agent
上下文里构造过滤后的 YAML 文件；只有用户明确要求 partial import 时，才用 `metadata spaces` 和 `metadata import`。

用户的主机在腾讯云上时，再按 `references/private-network.md`（[private-network.md](private-network.md)）检查 SCF 访问外部接入的路由。`setup init` 会在
写入 Admin 和元数据之前生成 `scf_routes`；后续 `collector function publish` 优先发布与外部接入同地域的函数并绑定
它的 VPC，跨地域走 `access@storage` 的公网地址，不创建 CCN。

## 凭据边界

禁止 Agent 把 `moox.toml` 全文读入对话、复制、打印或 `source`。具体来说，禁止使用 `cat moox.toml`、`sed moox.toml`、
`rg moox.toml`，不要用 Python 读取整个文件，也不要 `source moox.toml`。不要把它放进归档、通过其他进程转发、打印，
或从中派生 shell 变量。只有 `moox-cli setup` 可以读取其中的密钥。

CLI 可以报告带类型的状态码、主机名和已核验的指纹，但不能打印管理员密码、SSH 密码、腾讯云 SecretId/SecretKey、
会话令牌、请求签名密钥或生成的运行时密钥。
