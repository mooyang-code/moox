# moox-host-gateway

每台主机一个的服务网关：按原始 PB/JSON 字节校验 HMAC，依据 Admin 下发的完整快照和 `servicecatalog` ACL，将请求转发到本机固定 loopback 端口。

执行计划：[网关与服务部署重构](../../docs/计划/网关与服务部署重构执行计划.md)。

## 构建与测试

```bash
./scripts/build/build.sh host-gateway
go test -count=1 ./modules/hostgateway/...
```

## 配置

`config/app.yaml` 使用 `packages/servicecatalog/hostgatewayconfig` 的共享配置结构；`config/trpc_go.yaml` 配置刷新与指标定时器。离线 Admin bootstrap 输出主机配置、MooX CA、主机证书和调用方密钥，运行端只读取已有身份文件。

- `11003`：TLS tRPC，使用独立 MooX 私有 CA 验证主机 ID；服务请求仍须 HMAC。
- `127.0.0.1:11002`：本机 tRPC 转发与只读 Directory。Directory 仅包含主机和服务目录。
- `11012`：经过健康鉴权的 `/healthz`、`/readyz` 和指标入口。

control 网关直接访问 `127.0.0.1:11112`；其他主机经 control 的 TLS 网关同步。路由、目录和校验密钥一起保存在 `data/host-gateway/snapshot.pb`，Unix 目录和文件分别要求 0700、0600。控制面故障时使用最后有效缓存；同步或心跳超过 90 秒未成功会降低就绪状态。权限撤回先应用到内存，落盘失败会持续重试。nonce 使用独立的持久化存储。

```bash
moox-host-gateway-cli check-config --config hostgateway/config/app.yaml
moox-host-gateway-cli routes --config hostgateway/config/app.yaml
moox-host-gateway-cli health --config hostgateway/config/app.yaml
```

`routes` 只输出公共诊断字段，省略校验密钥。`health` 从配置推导本机 `/readyz` 地址，并使用健康鉴权环境变量；也可用 `--url` 指定地址。
