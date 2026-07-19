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

[control_host]
name = "control"
address = ""
port = 22
username = "ubuntu"
password = ""

[[other_hosts]]
name = "compute-1"
address = ""
port = 22
username = "ubuntu"
password = ""
```

Only `control_host` determines the first control-plane deployment. `other_hosts`
are imported as available hosts, but later service placement is decided after
Admin is working.

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
This phase ends after Admin, Gateway, and Web are ready, the manifest records are
written, and the public login API is valid.

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

## Phase 3: Optional Business Metadata

Storage deployment and metadata selection are separate from `custom.toml`
initialization. First list the selectable spaces from the checked-in seed:

```bash
./bin/moox-cli metadata spaces --file examples/metadata-quant-initial.seed.yaml
```

Ask which spaces the user wants. Support natural-language answers including
"全部", "只导入 A 股和币安", and "暂不导入". For a concrete
selection, map the names to the IDs returned by `metadata spaces`, then run:

```bash
./bin/moox-cli setup metadata-import \
  --file ./custom.toml \
  --seed examples/metadata-quant-initial.seed.yaml \
  --storage-host <host-name> \
  --spaces stock_cn,crypto_binance
```

Only `setup metadata-import` may use the manifest to open the SSH tunnel to the
selected Storage host. It imports the selected Space dependency closure and
uses create-if-missing semantics. Never construct a filtered YAML file in the
Agent context. If the user chooses "暂不导入", stop after Storage is ready.

## Secret Boundary

禁止 Agent 读取或解析 `custom.toml`. In particular, never use `cat custom.toml`,
`sed custom.toml`, `rg custom.toml`, Python 读取 the file, or `source custom.toml`.
Do not copy it into an archive, pipe it through another process, print it, or
derive shell variables from it. Only `moox-cli setup` may read the manifest.

The CLI may report typed status codes, host names, and verified fingerprints. It
must not print Admin passwords, SSH passwords, Tencent SecretId/SecretKey,
session tokens, request signing keys, or generated runtime secrets.
