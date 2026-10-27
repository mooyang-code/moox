# Custom Setup

Use this workflow for a new MooX installation. The user must create the manifest
before Admin, Gateway, or Web is deployed. The manifest is initialization input,
not a deployment artifact.

## User Preparation

推荐用本机表单生成 `moox.toml`，密码和云凭据不经过 Agent：

```bash
./bin/moox-cli config init
```

命令在 `127.0.0.1` 起一个一次性页面并用系统浏览器打开，用户分步填写：腾讯云密钥（先验证）→ 服务器
（数据存储、管理台；可选的海外服务器只在使用加密货币行情时需要；可选的因子机是局域网里的 Linux 机器，
运行因子引擎与归档 Worker）→ 管理员与告警 → 确认。腾讯云服务器只填公网 IP 和 SSH 登录方式，
地域与规格按 IP 从当前账号识别，不在账号下的 IP 不能用；因子机提交前要通过环境检测，首次 SSH 连接由
用户在页面上确认主机指纹。确认后直接写出仓库根目录的 `moox.toml`（权限 0600，**整份加密保存**，解密密钥在
系统钥匙串里：macOS 钥匙串、Windows 凭据管理器或 Linux Secret Service；没有钥匙串时是 `~/.config/moox/config.key`），
所有 `moox-cli` 命令会自动解密。规则：

- 腾讯云密钥建议用子用户（访问管理 → 用户列表，编程访问），需要授权 6 个预设策略：
  `QcloudSCFFullAccess`（采集云函数）、`QcloudCOSFullAccess`（函数代码包）、`QcloudCLSFullAccess`（日志）、
  `QcloudLighthouseFullAccess`（轻量服务器识别、防火墙、云联网、重启）、`QcloudCVMFullAccess`（CVM 识别、重启）、
  `QcloudVPCFullAccess`（弹性 IP、安全组、私有网络与云联网）。页面第 1 步列出了这张表；识别服务器或部署报权限
  不足时，先核对这 6 个策略是否都已授权。
- 目标文件已存在时命令拒绝覆盖；旧文件必须由用户自己改名备份，Agent 不读取、不改写。
- 命令只输出「已生成」、文件路径和主机 ID。Agent 不得读取生成的文件，也不得用自己的浏览器
  工具打开或填写这个页面。
- 系统浏览器没能自动打开时（例如远程环境），命令会在 stderr 打印页面地址；把地址原样转给用户，
  由用户在自己的浏览器里打开。页面默认 1 小时有效，页面顶部有倒计时；超时没有提交会自动关闭，不写任何文件，重新运行即可。
- 其余配置（保留期、日志、因子等）沿用示例默认值。文件加密后不能再手工编辑：用户要查看或修改配置时，
  运行 `./bin/moox-cli config edit`，在本机配置页里查看（密码默认隐藏）和用表单修改常用设置（保存前校验，保存后重新加密并同步备份）；页面不展示配置文件原文。
  Agent 同样不得打开或读取这个页面，命令只输出是否保存过、文件是否加密。
- 手工填写的明文 `moox.toml` 用 `./bin/moox-cli config encrypt` 原样加密保存；已加密的文件只核对能否解开。
- 初始化部署完成、管理台可以访问后，运行一次 `./bin/moox-cli config backup`：用 moox.toml 里的管理员账号登录管理台，
  把用管理员密码加密的整份配置备份到管理后台（服务端解不开）。之后在配置页保存时会自动同步。
- 用户换了电脑：运行 `./bin/moox-cli config restore`，用户在本机恢复页填管理台地址和管理员账号，CLI 取回备份、在本机解开，
  用新电脑的钥匙串密钥重新加密写成 `moox.toml`。Agent 同样不打开、不读取恢复页。恢复要用初始化时设置的管理员账号：
  管理员密码设定后不能修改（备份用它加密），需要别的账号时新建管理员，不影响备份。

也可以手工填写：让用户参照仓库里的 `moox.toml.example` 自己写出根目录的 `moox.toml`，再用
`./bin/moox-cli config encrypt` 加密保存。Agent 不代填、不读取。

### 拓扑怎么写

- `[hosts.<主机ID>]` 是唯一的主机目录：`label`（中文名）、`address`、`region`、`provider`、`ssh = { port, username }`，
  控制主机另有 `tls_mode`（`auto` 按地址选公网证书或内部 CA）。只能出站的主机写 `reachability = "outbound"`。
  主机的登记、启停与移除见 [`host-registry.md`](host-registry.md)。
- `[deployments]` 按主机 ID 列出业务组件；`host-gateway` 与 `host-agent` 由代码为每台部署主机自动补齐；
  因子机列 `factor-engine`、`archive-worker`（外部程序组件）。
- 构建一律在本机进行（含 CGO 的 Storage 在 Docker 里构建），环境见 `docs/构建手册.md`；`[compile_host]` 已退役，配置里出现会被拒绝。
- `[eventbus]` 只写端口与 TLS 开关，地址由 `[deployments]` 里 `eventbus` 所在的主机推出（默认放在存储机，它是独立单元，用 `setup deploy-unit --profile eventbus` 安装）；账号、token、CA 和私钥由 MooX 在部署时生成。
- `[paths]` 统一声明运行时根目录（默认 `/data/moox`）。
- `[notification]` 的 webhook 地址是凭据：留空时仍采集和展示监控数据，只是不发站外告警。

## Phase 1: Native Initialization

The Agent may inspect existence only, then follow `docs/部署与运维.md` 第 2 节 in order:

```bash
test -e ./moox.toml || exit 2
./bin/moox-cli setup validate --file ./moox.toml
./bin/moox-cli setup bootstrap --stage core --file ./moox.toml --source-dir .
./bin/moox-cli setup deploy-host --file ./moox.toml --source-dir . --host <主机ID>      # 每台非 control 主机
./bin/moox-cli setup build-linux --module storage --source-dir .
./bin/moox-cli setup deploy-unit --file ./moox.toml --source-dir . --host <主机ID> --profile storage --binary-dir ./bin
./bin/moox-cli setup deploy-unit --file ./moox.toml --source-dir . --host <主机ID> --profile access   # 以及 egress-proxy、trade、collector-stream
./bin/moox-cli setup bootstrap --stage control --file ./moox.toml --source-dir .
```

- 每个 `deploy-unit` 只安装 `[deployments]` 里登记在该主机上的单元；按实际部署逐台执行，不要自行挑主机。
- 因子机的 `factor-engine`、`archive-worker` 在 `setup init` 之后用 `setup deploy-unit --profile factor-engine|archive-worker --release <ID>` 部署，详见 [`host-registry.md`](host-registry.md)。
- 控制主机 `tls_mode = "internal"` 的首次安装要给 `bootstrap --stage control` 加 `--create-proxy-ca`；公网模式不加。
  internal CA 部署后用 `./bin/moox-cli setup trust-browser --file ./moox.toml` 让本机浏览器信任。
- 所有阶段使用同一个 `moox.toml` 和同一个 `--state-dir`；重试复用原请求。`*-paused` 不是 ready。
- 腾讯云端口用 `./bin/moox-cli setup firewall --file ./moox.toml` 放行。

If validation reports `host_key_unknown`, obtain each SHA256 host fingerprint
through an independent trusted channel, run `setup trust-host`, and repeat
validation. Never accept a fingerprint merely because the same SSH connection
reported it.

### macOS：初始化后让浏览器信任管理台证书

`setup init` 末尾的“浏览器信任”只检查证书是否进了钥匙串，不保证浏览器真的信任它。用户在 Mac 上操作时，初始化完成后要**主动提醒用户**这一步（internal TLS 才需要）：

```bash
security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db ~/.moox/certs/moox-caddy-root-<控制机地址>.crt
```

macOS 会弹出系统授权框（密码或指纹），不需要在终端里输密码；也可以在“钥匙串访问”里双击该证书，把“使用此证书时”设为“始终信任”。用 `security verify-cert -c <证书文件>` 能确认是否生效。
不要清理钥匙串里的这张证书（包括“清理旧 CA”时），否则浏览器访问管理台和登录接口会立刻报证书错误。用户不想改钥匙串时，改为在浏览器里对该站点点一次“继续访问”。

## Phase 3: Default Metadata Initialization

After Storage is ready, initialize the checked-in A-share and crypto spaces,
their Dataset and Field contracts, and the internal monitoring metadata:

```bash
./bin/moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host <主机ID>
```

Only `setup init` may use the manifest to open the SSH tunnels to Admin and the
selected Storage host. It creates or verifies the Admin business spaces and all
Storage metadata, activates ready Datasets, and verifies the result. Never
construct a filtered YAML file in the Agent context. Use `metadata spaces` and
`setup metadata-import` only when the user explicitly requests a partial import.

启用 SCF 时，按 `references/private-network.md` 为每个采集地域配置同地域的 Access 主机与 VPC。

## Secret Boundary

禁止 Agent 把 `moox.toml` 全文读入对话、复制、打印或 `source`。In particular, never use `cat moox.toml`,
`sed moox.toml`、`rg moox.toml`，不要把整个文件 Python 读取进对话，or `source moox.toml`。
Do not copy it into an archive, pipe it through another process, print it, or
derive shell variables from it. Only `moox-cli setup` may read secrets in the manifest.

EventBus、控制台代理和 `MOOX_PUBLIC_HOST` 按各自协议和证书配置，不因 SCF 网络改变。

The CLI may report typed status codes, host names, and verified fingerprints. It
must not print Admin passwords, SSH passwords, Tencent SecretId/SecretKey,
session tokens, request signing keys, or generated runtime secrets.
