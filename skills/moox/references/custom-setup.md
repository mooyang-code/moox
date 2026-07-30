# Custom Setup

Use this workflow for a new MooX installation. The user must create the manifest
before Admin, Gateway, or Web is deployed. The manifest is initialization input,
not a deployment artifact.

## User Preparation

Show the user `custom.toml.example`. The user must fill it as repository-root
`custom.toml` and protect it before the Agent continues:

```bash
chmod 0600 ./custom.toml
```

用户必须在部署前填写 `custom.toml`。该文件包含明文凭据，初始化完成后仍由用户保管；
CLI 对文件保持不变，不删除、不改写。

The checked-in template is:

```toml
[admin]
username = "admin"
password = ""

[tencent_cloud]
secret_id = ""
secret_key = ""

[eventbus]
public_address = ""
port = 4222
tls_enabled = true

# 留空时仍采集和展示监控数据，但不发送站外告警。
[monitoring]
wecom_webhook = ""

[control_host]
name = "control"
address = ""
port = 22
username = "ubuntu"
password = ""

[compile_host]
name = ""
address = ""
port = 0
username = ""
password = ""

[[other_hosts]]
name = "compute-1"
address = ""
port = 22
username = "ubuntu"
password = ""
```

`eventbus` 只包含公网连接事实：SCF 可访问的 IPv4 或 DNS 地址、监听端口和必须开启的
TLS。用户不填写 EventBus 账号、token、CA 或私钥；MooX 在部署时生成这些材料以及
`cloudnode-worker.yaml`。

`monitoring.wecom_webhook` 是唯一需要用户提前填写的监控专用信息。填写企微群机器人
HTTPS webhook 后，部署会以 mode `0600` 的运行时环境文件交给 Monitor；留空不会
关闭监控采集、查询和规则计算，只是不发送站外告警。

不在 `custom.toml` 中重复登记标准微服务、健康检查 URL、指标 subject 或实时
Dataset + Frequency。标准服务由 SysDeploy 默认清单维护；所有启用中的实时
TimeSeries Dataset + Frequency 由 Monitor 从 Collector 规则和 Factor binding
自动刷新。用户自行增加的非标准服务，应在控制面就绪后注册到 SysDeploy，而不是写进
初始化清单。

Only `control_host` determines the first control-plane deployment. `compile_host`
is optional, is used only by `scripts/build-storage-linux.sh` for native Linux
CGO builds, and is not a service-placement target. The native builder uses the
local SSH key/agent and the trusted host-key store; its `password` field is not
used by the shell transport. `other_hosts` are imported as available hosts, but
later service placement is decided after Admin is working. `control_host` and
every `other_hosts` entry are also the physical-server inventory for monitoring;
`compile_host` is not monitored unless it is also registered as a deployment host.
注册主机不会代替安装 HostAgent；需要采集 CPU、内存、磁盘的部署主机仍应按
`references/host-agent.md` 安装并启动 HostAgent。

## Phase 1: custom.toml And Control Initialization

The Agent may inspect existence only, then invoke these commands in order:

```bash
test -e ./custom.toml || exit 2
./bin/moox-cli setup validate --file ./custom.toml
./bin/moox-cli setup deploy-control --file ./custom.toml
./bin/moox-cli setup apply --file ./custom.toml
./bin/moox-cli setup status --file ./custom.toml
```

If validation reports `host_key_unknown`, obtain each SHA256 host fingerprint
through an independent trusted channel, run `setup trust-host`, and repeat
validation. Never accept a fingerprint merely because the same SSH connection
reported it.

`apply` is successful only when its JSON includes `login_api: valid`. Ask the
user to confirm browser login when interactive browser acceptance is required.
Do not ask about Storage or business metadata until status 返回 `completed`.
This phase ends after Admin, Gateway, Web, EventBus, CloudNode, and Collector are
ready, the manifest records are written, and the public login API is valid.

## Phase 2: Storage Host Selection

List the sanitized host choices through the CLI. This command may display host
names, addresses, ports, usernames, and roles, but never passwords:

```bash
./bin/moox-cli setup hosts --file ./custom.toml
```

Use 自然语言 to ask the user where Storage should run. Accept answers such
as "Storage 放到 compute-1" or "就放 control" and map only the selected host
name into the deterministic command:

```bash
./bin/moox-cli setup deploy-storage --file ./custom.toml --host <host-name>
```

The initial Storage unit contains `storage-primary` and the unified
`storage-view` process on the same selected machine. An optional private
`storage-shard` process may be placed on a separate machine. The View process owns
index, materialization, and query responsibilities behind one `trpc_go.yaml`.
Admin, Gateway, and Web remain on `control_host`. The command also updates the
Storage endpoints in SysDeploy. Never silently choose a Storage host.

## Phase 3: Default Metadata Initialization

After Storage is ready, initialize the checked-in A-share and crypto spaces,
their Dataset and Field contracts, and the internal monitoring metadata:

```bash
./bin/moox-cli setup init \
  --file ./custom.toml \
  --config-dir ./examples/setup/default \
  --storage-host <host-name>
```

Only `setup init` may use the manifest to open the SSH tunnels to Admin and the
selected Storage host. It creates or verifies the Admin business spaces and all
Storage metadata, activates ready Datasets, and verifies the result. Never
construct a filtered YAML file in the Agent context. Use `metadata spaces` and
`setup metadata-import` only when the user explicitly requests a partial import.

## Secret Boundary

禁止 Agent 读取或解析 `custom.toml`. In particular, never use `cat custom.toml`,
`sed custom.toml`, `rg custom.toml`, Python 读取 the file, or `source custom.toml`.
Do not copy it into an archive, pipe it through another process, print it, or
derive shell variables from it. Only `moox-cli setup` may read the manifest.

The CLI may report typed status codes, host names, and verified fingerprints. It
must not print Admin passwords, SSH passwords, Tencent SecretId/SecretKey,
session tokens, request signing keys, or generated runtime secrets.
