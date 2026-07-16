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

## Agent-Safe Sequence

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
Begin incremental service placement only after status 返回 complete 后. Ask for
each later service host when that deployment step begins rather than expanding
`custom.toml`.

## Secret Boundary

禁止 Agent 读取或解析 `custom.toml`. In particular, never use `cat custom.toml`,
`sed custom.toml`, `rg custom.toml`, Python 读取 the file, or `source custom.toml`.
Do not copy it into an archive, pipe it through another process, print it, or
derive shell variables from it. Only `moox-cli setup` may read the manifest.

The CLI may report typed status codes, host names, and verified fingerprints. It
must not print Admin passwords, SSH passwords, Tencent SecretId/SecretKey,
session tokens, request signing keys, or generated runtime secrets.
