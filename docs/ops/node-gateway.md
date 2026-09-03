# Node Gateway 运维手册

> 本手册对应当前“一个中央 Admin + 每台服务器一个独立 Gateway”的已实现架构。组件边界、路由模型和失败语义见[节点服务网关架构](../节点服务网关架构.md)。

本文覆盖广州 `gateway-gz-122`（`106.53.107.122`）和香港
`gateway-hk-177`（`43.132.204.177`）两个节点。部署目录固定为
`/home/ubuntu/moox/prod`。Gateway 只监听本机 `127.0.0.1:11002`，诊断端口为
`127.0.0.1:11012`；公网请求必须经过 Caddy HTTPS。

## 健康检查

在每台服务器执行：

```bash
cd /home/ubuntu/moox/prod
./status.sh gateway
./healthcheck.sh gateway
set -a; source ./secrets/health-auth.env; set +a
./bin/moox-gateway-cli health --url http://127.0.0.1:11012/readyz
```

`healthz` 证明进程及持久化目录可用；`readyz` 同时证明已加载有效路由，且最近一次
控制面路由同步和心跳确认均未超过 90 秒。进程可能仍在使用缓存路由，但如果控制面
失联，`readyz` 会返回 503，避免把“进程在线”误判为“节点健康”。检查监听归属：

```bash
sudo ss -lntp | grep -E ':(443|9527|11002|11012|11409|11410)\b'
```

预期只有 Caddy 监听公网 HTTPS；`11002`、`11012`、`11409`、`11410` 均为
loopback。外部主机还应验证业务端口不可达：

```bash
for host in 106.53.107.122 43.132.204.177; do
  for port in 11002 11012 11409 11410; do
    nc -zvw3 "$host" "$port" && echo "UNEXPECTED OPEN $host:$port" || true
  done
done
```

## 节点与服务注册

在 `服务管理 -> 网关节点` 创建或编辑以下记录：

| node_id | 主机 | 名称 | public_address | status |
| --- | --- | --- | --- | --- |
| `gateway-gz-122` | 腾讯云-122 | 广州节点 | `https://106.53.107.122` | `enabled` |
| `gateway-hk-177` | 腾讯云-香港 | 香港节点 | `https://43.132.204.177` | `enabled` |

在 `服务管理 -> 服务实例` 登记本机服务。Gateway 暴露的记录必须满足：

- `node_id` 是服务所在节点；
- `protocol=http`，`host` 只能是 `127.0.0.1` 或 `::1`；
- `status=active` 且 `gateway_enabled=true`；
- `gateway_service_id` 在同一节点唯一；
- Monitor 使用 `gateway_service_id=monitor`、端口 `11410`、
  `gateway_path=trpc.moox.monitor.MonitorMgr`。

保存后无需发布。Gateway 最迟在 15 秒后的下一次拉取应用新快照。注册完成后，在
`网关节点` 查看 `route_hash`、`applied_route_hash`、路由数、最近心跳和错误；两个 hash
必须一致。

V1 只部署一个 Monitor。部署脚本为所有非 Storage 进程注入
`<service>@<node>` canonical instance ID、node ID 和每次启动新生成的 boot ID；不得用
hostname/PID 回退，也不得配置 Monitor peer。

## 两节点部署命令

以下命令用于正常重复发布，不删除现有数据。Gateway 是所有节点的默认组件；香港使用
`--no-admin`，因此不包含浏览器站点、Admin schema 和 Admin 凭据。

只有明确要重建未上线环境时才额外使用 `--reset-data`。该参数会删除整个目标数据目录；
执行前必须备份 Admin 数据库并导出 `t_ssh_host`：

先在广州节点创建带时间戳的完整数据库备份，并把主机配置导出到本机：

```bash
ssh ubuntu@106.53.107.122 'cp /home/ubuntu/moox/prod/data/admin.db /home/ubuntu/moox/prod/data/admin.db.pre-gateway-$(date +%Y%m%d%H%M%S)'
ssh ubuntu@106.53.107.122 'sqlite3 /home/ubuntu/moox/prod/data/admin.db ".mode insert t_ssh_host" "select * from t_ssh_host;"' > /tmp/moox-ssh-hosts.sql
```

准备好双 CA bundle、control/service key 和 Admin 密码文件后，先部署广州。CA bundle
必须包含控制端和对端 Caddy 根证书；部署脚本会在实际启动前用该 bundle 校验
`--gateway-control-url`，不能使用 `moox-control-package-*` 这类 Gateway 内部证书代替
Caddy 根证书：

```bash
./scripts/deploy/deploy-moox.sh \
  --target ubuntu@106.53.107.122 --dir /home/ubuntu/moox/prod \
  --public-host 106.53.107.122 --service-https-port 443 \
  --node-id gateway-gz-122 \
  --gateway-control-url https://106.53.107.122:9527 \
  --gateway-ca-bundle /tmp/moox-gateway-peers.pem \
  --gateway-control-key-file /tmp/moox-gateway-control.key \
  --gateway-service-key-file /tmp/moox-gateway-service.key \
  --monitor-instance-id moox_monitor@gateway-gz-122 \
  --admin-password-file /tmp/moox-admin-password
```

部署脚本会检查目标机是否已有其他目录启动的 Gateway。正常启动部署会先停止旧进程并
记录迁移；`--no-start` 只生成或安装包时，如果发现端口上的旧 Gateway，则直接拒绝，
避免双进程争抢端口。启动完成后脚本还会等待 `readyz`、路由缓存和节点 ID 验收通过。
组件覆盖发布会在验收前保留旧 Gateway 文件；若新进程未能通过控制面验收，脚本会自动
恢复旧文件并重新启动旧 Gateway，避免失败发布留在线上。

若本次确实使用了 `--reset-data`，部署成功并完成新库初始化后立即恢复主机配置：

```bash
ssh ubuntu@106.53.107.122 'sqlite3 /home/ubuntu/moox/prod/data/admin.db' < /tmp/moox-ssh-hosts.sql
```

创建香港网关节点后再部署香港数据面；不要在第二个节点启用 Monitor：

```bash
./scripts/deploy/deploy-moox.sh \
  --target ubuntu@43.132.204.177 --dir /home/ubuntu/moox/prod \
  --public-host 43.132.204.177 --service-https-port 443 \
  --node-id gateway-hk-177 \
  --gateway-control-url https://106.53.107.122:9527 \
  --gateway-ca-bundle /tmp/moox-gateway-peers.pem \
  --gateway-control-key-file /tmp/moox-gateway-control.key \
  --gateway-service-key-file /tmp/moox-gateway-service.key \
  --no-admin --no-web-host --no-monitor --no-storage --no-archive --no-eventbus \
  --no-cloudnode --no-collector --no-factor
```

部署脚本拒绝 `--monitor-peer`。Monitor 需要完整 EventBus/metrics 链路，不存在
`peer-only` 运行模式。远程节点检查通过 SSH 登录目标节点后运行 Doctor CLI。

## 路由检查

在节点本机检查当前已应用且校验通过的缓存：

```bash
cd /home/ubuntu/moox/prod
./bin/moox-gateway-cli check-config --config gateway/config/app.yaml
./bin/moox-gateway-cli routes --config gateway/config/app.yaml | jq .
jq '{node_id,route_hash,disabled,routes}' data/gateway/routes.json
```

中央 Admin 数据库可只读核对控制面状态：

```bash
sqlite3 /home/ubuntu/moox/prod/data/admin.db \
  "select c_node_id,c_status,c_route_hash,c_applied_route_hash,c_route_count,c_last_seen_at,c_last_error from t_gateway_nodes order by c_node_id;"
sqlite3 /home/ubuntu/moox/prod/data/admin.db \
  "select c_node_id,c_service_name,c_gateway_service_id,c_host,c_port,c_gateway_path,c_status,c_gateway_enabled from t_service_deployments order by c_node_id,c_service_name;"
```

不要手工修改 `data/gateway/routes.json`。配置错误应在服务管理页面修正；Gateway 会拒绝
整份非法快照并继续使用上一份有效配置。

## Doctor 验收

在 Monitor 所在节点运行 `./bin/moox-cli doctor bootstrap --format json`，等待两个 Reporter
周期后运行 `./bin/moox-cli doctor diagnose --format json`。远程节点通过 SSH 在目标节点执行；
`bootstrap --node` 只能使用本机 node ID。Context 和健康直读分别受 service HMAC 与 health
HMAC 保护，公开诊断路径仍返回 `404`。

## 密钥替换

系统只使用当前 key，不做双 key 兼容。control key 由 Admin 和两台 Gateway 共享；
service key 由两台 Gateway 和授权机器调用方共享。两把 key
必须不同，文件必须为 `0600`。

```bash
umask 077
openssl rand -hex 32 > /tmp/moox-gateway-control.key
openssl rand -hex 32 > /tmp/moox-gateway-service.key
test "$(cat /tmp/moox-gateway-control.key)" != "$(cat /tmp/moox-gateway-service.key)"
```

替换时安排一次短暂停机：先停止单实例 Monitor，再停止两台 Gateway，然后停止中央
Admin；把同一份新 control key 安装到 Admin 和两台 Gateway，把同一份新 service key
安装到两台 Gateway 和授权调用方；最后按下一节顺序启动。不要把 key 写入 YAML、命令
输出或日志，也不要复制任一 Caddy CA 私钥。部署脚本的
`--gateway-control-key-file` 和 `--gateway-service-key-file` 会按正确权限安装原始 key 与环境文件。

## 重启顺序

完整集群使用以下顺序，避免 Monitor 在 Gateway 尚未 ready 时产生无意义故障窗口：

1. 广州基础设施服务和 Admin；
2. 广州 Gateway，等待 `readyz`；
3. 香港 Gateway，等待 `readyz`；
4. 单实例 Monitor；
5. 运行 Doctor 并核对 Admin route hash。

单节点操作使用部署目录脚本：

```bash
./stop.sh monitor
./restart.sh gateway
./healthcheck.sh gateway
./start.sh monitor
```

Admin 故障期间不要删除 `data/gateway/routes.json`；Gateway 会继续使用缓存。无缓存的新节点
必须先恢复 Admin，首次拉取成功后才能 ready。

## Monitor 故障演练

1. 先运行 bootstrap 和 diagnose，保存通过报告。
2. 停止单实例 Monitor，保持业务服务和 Gateway 运行。
3. `doctor diagnose` 必须返回 `INCONCLUSIVE` 和 `run_bootstrap`，不能静默执行另一套诊断。
4. 在同节点运行 `doctor bootstrap`，确认业务 health 仍可检查且 Monitor 故障独立显示。
5. 恢复 Monitor，等待两个 Reporter 周期，再次运行 diagnose。

本地自动化对应命令：

```bash
go test -count=1 ./modules/gateway/test ./modules/admin/test ./modules/monitor/test
```

远端验收记录应包含时间、操作节点、route hash、Doctor run ID、故障/恢复结果和端口检查。
