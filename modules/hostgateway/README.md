# moox-host-gateway

每台主机一个的服务网关：按原始 PB/JSON 字节校验调用方的 ed25519 签名（只持有公钥），按 Admin 下发的主机与部署和内嵌的 `servicecatalog` 自己算路由与调用名单，将别的主机发来的请求转发到本机固定 loopback 端口。登记成外部入口的主机另开 `11004`，接外部身份（云函数、Skill、采集名单工具）的签名请求。

## 构建与测试

```bash
./scripts/build/build.sh host-gateway
go test -count=1 ./modules/hostgateway/...
```

## 配置

`config/app.yaml` 使用 `packages/servicecatalog/hostgatewayconfig` 的共享配置结构；`config/trpc_go.yaml` 配置刷新与指标定时器。`app.yaml` 由部署按拓扑渲染；MooX CA 根证书、主机证书和主机签名私钥来自控制机签发的身份包，运行端只读取已有身份文件。

- `11003`：TLS tRPC，用 MooX CA 签的主机证书，客户端按主机 ID 校验；服务请求仍须调用方签名。只能出站的主机（登记为 `outbound`）在同一端口上向 control 保持反向连接，别的主机访问它时由 control 中转字节，TLS 与签名仍是调用方到目标网关端到端的。
- `127.0.0.1:11002`：只读 Directory（登记的主机与部署）与 SSH 隧道进来的 `moox-cli` 请求。同机组件互调不经网关，直连服务的回环端口。
- `11004`：外部入口，明文 tRPC 加签名，只有 `server.external_addr` 写了才监听（部署按主机登记的 `external_entry` 渲染）。只接组件目录里的外部身份调名单上的方法；
  目标服务在本机就转本机，在别的主机就以 `host-gateway` 的身份带 `X-Moox-Origin-Caller` 头转到目标主机的 `11003`。
- `11012`：`/healthz`、`/readyz` 和指标入口，只听 127.0.0.1、不验签。

control 网关直接访问 `127.0.0.1:11112`；其他主机经 control 的 TLS 网关同步。转给本机服务时剥离全部 `x-moox-*` 字段；只有 `GatewayControl` 另外收到网关写的 `X-Moox-Verified-Host`（验过的签名主机 ID），Admin 自己不验签。

快照默认每 5 秒刷新，客户端 Directory 默认每 5 秒刷新，为 15 秒部署可见性留出处理时间。收到的快照（主机、部署、公钥）保存在 `data/host-gateway/snapshot.pb`，加载时重新算路由，Unix 目录和文件分别要求 0700、0600。控制面故障时使用最后有效缓存；同步或心跳超过 90 秒未成功会降低就绪状态。权限撤回先应用到内存，落盘失败会持续重试。nonce 记在进程内存里。

真实 Admin 联调单独运行，避免业务模块互相导入内部实现（`make test-e2e-gates` 会现编译并运行它，下面是分步做法）。测试位于 Admin 的 `gatewaycontrol` 包，使用 `hostgateway_e2e` 构建标签；以真实 SQLite、PKI、GatewayControl 和生产网关二进制运行。执行端要求 Linux 的两个 loopback IP，预留 `127.0.0.1:11112/20102/11402`、`127.0.0.{1,2}:11002/11003/11012` 和 `127.0.0.2:11004`。纯 Go 产物可在本机交叉编译后传到 Linux，运行脚本不编译：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -ldflags '-X main.Version=control-e2e' -o /tmp/moox-host-gateway-e2e ./modules/hostgateway/cmd/server
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c \
  -tags hostgateway_e2e -o /tmp/moox-admin-gateway-e2e ./modules/admin/internal/service/gatewaycontrol
# 在 Linux 的源码目录运行，变量使用该机器上的产物绝对路径。
MOOX_HOST_GATEWAY_E2E_BINARY=/tmp/moox-host-gateway-e2e \
MOOX_HOST_GATEWAY_E2E_TEST_BINARY=/tmp/moox-admin-gateway-e2e \
bash scripts/test/gates/test-host-gateway-control-e2e.sh
```

脚本逐项核对七组场景的执行标记（含外部入口：本机转发、跨主机转发、名单外方法被拒、伪造来源头被拒）；缺少产物、标签或任何场景均失败。测试实际等待 90 秒陈旧阈值，整体时限为 6 分钟。

```bash
moox-host-gateway-cli check-config --config host-gateway/config/app.yaml
moox-host-gateway-cli routes --config host-gateway/config/app.yaml
moox-host-gateway-cli health --config host-gateway/config/app.yaml
moox-host-gateway-cli sign-public-key --key-file secrets/host.key
```

`routes` 输出按落盘快照算出的本机路由、目录与不认识的组件，不含公钥。`health` 请求配置里本机的 `/readyz` 地址；也可用 `--url` 指定地址。`sign-public-key` 输出签名私钥对应的公钥，部署时用它核对目标机上现有的密钥。
