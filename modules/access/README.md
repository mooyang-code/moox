# 外部接入

`moox-access` 是外部调用方的签名 tRPC 入口，监听 `11004`，目标身份为 `access@<主机 ID>`。它从组件目录读取 `scf-collector`、`factor-engine`、`archive-worker`、`collector-operator` 和 `moox-skill` 的逐方法白名单，校验 Admin 分配的 caller/KeyID、签名和目标身份，以持久化 nonce 拒绝重放，再通过一个进程级 `gatewayclient` 以内部 `access` 身份转发请求。

各外部调用方固定连接一个 Access 实例。Access 使用主机网关目录选择业务服务所在主机，同机走 loopback 原生 tRPC，跨主机走私有 CA 校验的 TLS tRPC。外部调用方只携带自己的签名身份；Storage 请求体内的角色认证独立保留。Access 转发原始 PB/JSON 字节及空间、trace 元数据，丢弃外部提供的用户权限元数据。

```bash
CGO_ENABLED=0 go -C modules/access test ./...
TARGET_GOOS=linux TARGET_GOARCH=amd64 bash scripts/build/build.sh access
```

运行时读取 `access/config/app.yaml` 和 `access/config/trpc_go.yaml`，也可通过 `-config`、`-conf` 指定绝对路径。应用配置包含：

- `gateway_client`：内部 caller 固定为 `access`，KeyID 由 Admin 分配，`key_file` 相对应用配置目录解析。
- `verification_file`：Admin 导出的外部调用方校验文件；配置和密钥均为普通 `0600` 文件。
- `nonce_path`：持久化 SQLite nonce 数据库。重启保留 nonce，数据库故障时拒绝请求。

内部主机身份、loopback 地址和 CA 来自同一发布根目录下的 `host-gateway/config/app.yaml`。目录缓存位于 `data/access/gatewayclient`；控制面暂时不可达时继续使用有效缓存。就绪状态要求外部白名单涉及的服务都有目录 deployment。

健康检查使用 `11014`：仓库模板绑定 loopback，原生安装按组件目录渲染为 `0.0.0.0`；接口使用共享健康鉴权的 `MOOX_HEALTH_AUTH_*` 配置。逐方法超时与请求体上限来自组件目录，拒绝指标为 `moox_access_denials_total`。独立部署 profile 为 `access`，本组件使用纯 Go SQLite，可在本机关闭 CGO 构建 Linux 制品。

原生 `setup deploy-unit --profile access` 已接入身份导出、校验文件安装和配置渲染；实际网络放行与运行状态以部署结果和健康检查为准。外部调用方的完整逐方法白名单见[外部接入](../../docs/模块/外部接入.md)。
