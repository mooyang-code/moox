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

control 网关直接访问 `127.0.0.1:11112`；其他主机经 control 的 TLS 网关同步。只有 `GatewayControl` 保留已验证的六个签名字段，供 Admin 再次验证原调用主机和原始请求；该转发层不重试，下一次同步由调用方生成新 nonce。普通业务上游剥离所有 `x-moox-*` 字段。

快照默认每 5 秒刷新，客户端 Directory 默认每 5 秒刷新，为 15 秒部署可见性留出处理时间。路由、目录和校验密钥一起保存在 `data/host-gateway/snapshot.pb`，Unix 目录和文件分别要求 0700、0600。控制面故障时使用最后有效缓存；同步或心跳超过 90 秒未成功会降低就绪状态。权限撤回先应用到内存，落盘失败会持续重试。nonce 使用独立的持久化存储。

真实 Admin 联调是单独的必跑门禁，避免业务模块互相导入内部实现。测试位于 Admin 的 `gatewaycontrol` 包，使用 `hostgateway_e2e` 构建标签；以真实 SQLite、Badger、PKI、GatewayControl 和生产网关二进制运行。执行端要求 Linux 的两个 loopback IP，预留 `127.0.0.1:11112/20102` 和 `127.0.0.{1,2}:11002/11003/11012`。纯 Go 产物可在本机交叉编译后传到 Linux，运行脚本不编译：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go -C modules/hostgateway build \
  -ldflags '-X main.Version=control-e2e' -o /tmp/moox-host-gateway-e2e ./cmd/server
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go -C modules/admin test -c \
  -tags hostgateway_e2e -o /tmp/moox-admin-gateway-e2e ./internal/service/gatewaycontrol
# 在 Linux 的源码目录运行，变量使用该机器上的产物绝对路径。
MOOX_HOST_GATEWAY_E2E_BINARY=/tmp/moox-host-gateway-e2e \
MOOX_HOST_GATEWAY_E2E_TEST_BINARY=/tmp/moox-admin-gateway-e2e \
make test-host-gateway-control-e2e
```

门禁逐项核对六组场景的执行标记；缺少产物、标签或任何场景均失败。测试实际等待 90 秒陈旧阈值，整体时限为 6 分钟。

```bash
moox-host-gateway-cli check-config --config hostgateway/config/app.yaml
moox-host-gateway-cli routes --config hostgateway/config/app.yaml
moox-host-gateway-cli health --config hostgateway/config/app.yaml
```

`routes` 只输出公共诊断字段，省略校验密钥。`health` 从配置推导本机 `/readyz` 地址，并使用健康鉴权环境变量；也可用 `--url` 指定地址。
