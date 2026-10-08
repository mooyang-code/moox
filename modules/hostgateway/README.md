# moox-host-gateway

每台主机一个的主机网关，是 MooX 中唯一的网关：

- **跨主机入口**（`0.0.0.0:11003`，TLS，证书由 MooX 私有 CA 签发）与**本机入口**（`127.0.0.1:11002`）：
  校验调用方签名和 nonce，按快照中的路由与 ACL 把请求透传到本机组件，并写入可信的 `x-moox-verified-caller`；
- **服务目录**（本机入口上的 `trpc.moox.hostgateway.Directory`）：告诉本机的 gatewayclient 每个服务部署在哪台主机；
- **健康端口**（`0.0.0.0:11012`）：`/healthz`、`/readyz`、`/metrics`。

快照（路由、目录、校验密钥）由 Admin 的网关控制按组件目录和部署表编译，主机网关每 15 秒拉取一次并落盘缓存；
网关控制不可用时继续使用缓存，连续 90 秒同步失败后 `/readyz` 报告未就绪。control 主机直连本机的网关控制
（`127.0.0.1:11112`），其他主机经 control 的 11003 访问。

设计文档：[网关与服务部署重构设计](../../docs/计划/网关与服务部署重构设计.md)

## 构建与测试

```bash
./scripts/build/build.sh host-gateway
go test -count=1 ./modules/hostgateway/...
```

## 配置

- `config/app.yaml`：主机 ID、监听地址、TLS 证书、网关控制地址与调用方密钥、缓存目录；
- `config/trpc_go.yaml`：快照刷新与指标上报两个定时器。

## 运维命令

```bash
moox-host-gateway-cli check-config --config config/app.yaml   # 校验配置
moox-host-gateway-cli snapshot --config config/app.yaml       # 查看缓存的快照（不输出密钥）
moox-host-gateway-cli health --url http://127.0.0.1:11012/readyz
```
