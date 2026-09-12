# 腾讯云内网组网

打通 Control / Storage / Compute 与 SCF 的腾讯云内网。命令入口是
`moox-cli setup private-network`（别名 `moox-cli ops tencent private-network`）。
先读 `moox.toml.example` 里的 `storage_private_gateway_host` 注释，再对用户的
`moox.toml` 做同样字段的手术式修改。

## 何时使用

- 用户要求打通云联网 / CCN / SCF VPC / 内网组网
- 国内主机或国内 SCF 仍在拨 Storage 公网 IP，需要改走内网
- 需要探测同区域 TCP，或把已绑定 VPC 的国内函数网关改成内网 IP

不要用它做：改 EventBus 证书、Caddy/`MOOX_PUBLIC_HOST`、跨境把海外主机/函数切到国内内网 IP。

## 红线

- 不要调用 `ModifyInstancesVpcAttribute`（会停机）。
- SSH 和控制台入口继续用公网 IP。
- 账号未开通跨境 CCN 时必须两张云联网：国内 `moox-private-network`，海外 `moox-private-network-global`。不要把南京 Storage 和香港/新加坡/东京放进同一张 CCN。
- `storage_gateway_host` 保持 **公网** Storage IP。同一 Space 可能同时覆盖国内和海外地域。
- 组网后只填 `storage_private_gateway_host`（必须是 `net.IP.IsPrivate()` 的内网 IP，不能是公网或 loopback）。
- 不要改 EventBus `tls://<公网>:4222`、Caddy、`MOOX_PUBLIC_HOST`、compute-1 的公网入口。
- CLI **不改** `moox.toml`。Agent 也不得 `cat`/`source` 整个文件，或把密码、`secret_id`、`secret_key` 打进对话。

## 固定步骤

在仓库根目录执行。先 dry-run，确认 JSON 后再写云。

```bash
./bin/moox-cli setup private-network --file ./moox.toml --dry-run
./bin/moox-cli setup private-network --file ./moox.toml --update-scf-gateway
./bin/moox-cli setup private-network --file ./moox.toml --probe-only
```

`--update-scf-gateway` 只改 **与 Storage 同 area（mainland）** 且已绑定 VPC 的函数环境变量。海外函数保持公网网关。

常用开关：`--skip-scf`、`--skip-hosts`、`--skip-probe`、`--rewrite-runtime`、`--ccn-name`、`--probe-regions`。
`--rewrite-runtime` 只改 Control 上 `runtime.env` 的 Storage RPC 并重启 Collector，不会改 Factor/Monitor，也不改 EventBus。

命令会：发现主机 VPC/内网 IP、创建或复用两张 CCN、放通同区域安全组/轻量防火墙、为缺 VPC 的 SCF 地域建专用 VPC 并绑定函数。默认端口含 `4222`、`11003`、`11012`、`20100`、`20200`、`20201`、`20202`。

## 填写 moox.toml

从 `--dry-run` 或成功 apply 的 JSON 读取 `recommended_config.storage_private_ip`。
用户明确要求写入私有 IP 时，只改 `[[scf_fetcher.spaces]]` 的这一行，然后校验：

```toml
storage_gateway_host = "203.0.113.11"
storage_private_gateway_host = "10.0.0.5"
```

```bash
./bin/moox-cli setup validate --file ./moox.toml
```

校验成功后只回显每个 space 的 `space_id`、`storage_gateway_host`、`storage_private_gateway_host`。
不要打印其它字段。填好后，后续 `collector function publish` 会按地域选网关：国内用内网，海外用公网。

## 验收

`--probe-only` 的 `probes` 里，同 area 且 `expect=open` 的项必须是 `open`。跨境项预期 `closed`。

业务侧：

- Control → Storage 内网 `11003` 应有 ESTAB；Collector/Factor/Monitor 的 Storage gateway 应是 `ip://<storage-private-ip>:11003`。
- 国内 SCF `MOOX_STORAGE_RPC_GATEWAY_TARGET` 应是内网；香港/新加坡/东京应仍是公网。
- Compute-1 到 Storage 内网应失败，到 Storage 公网应成功。

`runtime_config_hits` 里出现 EventBus、Caddy、`MOOX_PUBLIC_HOST`、trade gateway 的公网 IP 是预期残留，不要据此改成内网。
