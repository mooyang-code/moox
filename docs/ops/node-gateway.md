# Node Gateway 运维手册

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
curl --fail --silent --show-error http://127.0.0.1:11012/healthz
curl --fail --silent --show-error http://127.0.0.1:11012/readyz
./bin/moox-gateway-cli health --url http://127.0.0.1:11012/readyz
```

`healthz` 证明进程及持久化目录可用；`readyz` 证明至少加载过一份有效路由。
Admin 暂时不可用不会让已有路由变为 not ready。检查监听归属：

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

两台 Monitor 必须使用稳定实例 ID，且各自只配置对端。广州配置为
`instance.instance_id=monitor-gz-122`，peer 为
`{instance_id: monitor-hk-177, gateway_url: https://43.132.204.177, node_id: gateway-hk-177}`；
香港配置为 `instance.instance_id=monitor-hk-177`，peer 为
`{instance_id: monitor-gz-122, gateway_url: https://106.53.107.122, node_id: gateway-gz-122}`。
不要使用默认的 hostname/PID 实例 ID；进程重启后它会变化，无法作为对端配置标识。

## 两节点部署命令

> **数据丢失警告：** 广州部署命令包含 `--reset-data`，会删除并重建未上线环境的
> Admin 数据库。未完成下面的数据库备份和 `t_ssh_host` 导出时，禁止执行部署命令。

先在广州节点创建带时间戳的完整数据库备份，并把主机配置导出到本机：

```bash
ssh ubuntu@106.53.107.122 'cp /home/ubuntu/moox/prod/data/admin.db /home/ubuntu/moox/prod/data/admin.db.pre-gateway-$(date +%Y%m%d%H%M%S)'
ssh ubuntu@106.53.107.122 'sqlite3 /home/ubuntu/moox/prod/data/admin.db ".mode insert t_ssh_host" "select * from t_ssh_host;"' > /tmp/moox-ssh-hosts.sql
```

准备好双 CA bundle、control/service key 和 Admin 密码文件后，先部署广州：

```bash
./scripts/deploy-moox.sh \
  --target ubuntu@106.53.107.122 --dir /home/ubuntu/moox/prod \
  --public-host 106.53.107.122 --service-https-port 443 \
  --node-id gateway-gz-122 \
  --gateway-control-url https://106.53.107.122:9527 \
  --gateway-ca-bundle /tmp/moox-gateway-peers.pem \
  --gateway-control-key-file /tmp/moox-gateway-control.key \
  --gateway-service-key-file /tmp/moox-gateway-service.key \
  --monitor-instance-id monitor-gz-122 \
  --monitor-peer monitor-hk-177,https://43.132.204.177,gateway-hk-177 \
  --admin-password-file /tmp/moox-admin-password --reset-data
```

广州部署成功并完成新库初始化后，立即恢复主机配置：

```bash
ssh ubuntu@106.53.107.122 'sqlite3 /home/ubuntu/moox/prod/data/admin.db' < /tmp/moox-ssh-hosts.sql
```

创建香港网关节点和本机 Monitor 路由后，再部署香港：

```bash
./scripts/deploy-moox.sh \
  --target ubuntu@43.132.204.177 --dir /home/ubuntu/moox/prod \
  --public-host 43.132.204.177 --service-https-port 443 \
  --node-id gateway-hk-177 \
  --gateway-control-url https://106.53.107.122:9527 \
  --gateway-ca-bundle /tmp/moox-gateway-peers.pem \
  --gateway-control-key-file /tmp/moox-gateway-control.key \
  --gateway-service-key-file /tmp/moox-gateway-service.key \
  --monitor-instance-id monitor-hk-177 \
  --monitor-peer monitor-gz-122,https://106.53.107.122,gateway-gz-122 \
  --no-admin --no-web-host --no-storage --no-archive --no-eventbus \
  --no-cloudnode --no-collector --no-factor
```

`--monitor-peer` 可重复传入；部署脚本严格校验三元组、稳定 ID 和 URL，并把实例 ID 与
peer 列表写入 Monitor 配置。HTTPS peer URL 不能带账号、路径、查询或 fragment；明文
HTTP 只允许 loopback。

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

## 签名验收请求

下面的函数使用节点部署的 service key 调用 Monitor 正式 RPC。它不会输出密钥：

```bash
signed_monitor_snapshot() {
  host=$1 node_id=$2 root=$3
  set -a; source "$root/secrets/gateway-service.env"; set +a
  path=/api/service/monitor/GetPeerSnapshot
  body='{}'
  timestamp=$(date +%s)
  nonce=$(openssl rand -hex 32)
  body_hash=$(printf %s "$body" | openssl dgst -sha256 | awk '{print $NF}')
  canonical=$(printf 'moox-gateway-auth-v1\nPOST\n%s\n%s\n%s\n%s\n%s' \
    "$path" "$body_hash" "$timestamp" "$nonce" "$node_id")
  signature=$(printf %s "$canonical" | openssl dgst -sha256 \
    -hmac "$MOOX_GATEWAY_SERVICE_SECRET_KEY" | awk '{print $NF}')
  curl --fail --silent --show-error --cacert "$root/certs/gateway/peers.pem" \
    -H 'Content-Type: application/json' \
    -H "X-Moox-Key-Id: $MOOX_GATEWAY_SERVICE_KEY_ID" \
    -H "X-Moox-Timestamp: $timestamp" -H "X-Moox-Nonce: $nonce" \
    -H "X-Moox-Target-Node: $node_id" -H "X-Moox-Signature: $signature" \
    --data "$body" "https://$host$path"
}

cd /home/ubuntu/moox/prod
signed_monitor_snapshot 106.53.107.122 gateway-gz-122 "$PWD" | jq .
signed_monitor_snapshot 43.132.204.177 gateway-hk-177 "$PWD" | jq .
```

响应必须包含成功的 `ret_info`、正确的 `instance_id`、新鲜的 `observed_at`、检查结果和
最近告警事件。把广州签名中的 target node 改为香港（或反向操作）必须得到 HTTP 401。

## 密钥替换

系统只使用当前 key，不做双 key 兼容。control key 由 Admin 和两台 Gateway 共享；
service key 由两台 Gateway 和所有机器调用方（目前包括两台 Monitor）共享。两把 key
必须不同，文件必须为 `0600`。

```bash
umask 077
openssl rand -hex 32 > /tmp/moox-gateway-control.key
openssl rand -hex 32 > /tmp/moox-gateway-service.key
test "$(cat /tmp/moox-gateway-control.key)" != "$(cat /tmp/moox-gateway-service.key)"
```

替换时安排一次短暂停机：先停止两台 Monitor，再停止两台 Gateway，然后停止中央
Admin；把同一份新 control key 安装到 Admin 和两台 Gateway，把同一份新 service key
安装到两台 Gateway 和两台 Monitor；最后按下一节顺序启动。不要把 key 写入 YAML、命令
输出或日志，也不要复制任一 Caddy CA 私钥。部署脚本的
`--gateway-control-key-file` 和 `--gateway-service-key-file` 会按正确权限安装原始 key 与环境文件。

## 重启顺序

完整集群使用以下顺序，避免 Monitor 在 Gateway 尚未 ready 时产生无意义故障窗口：

1. 广州基础设施服务和 Admin；
2. 广州 Gateway，等待 `readyz`；
3. 香港 Gateway，等待 `readyz`；
4. 广州 Monitor；
5. 香港 Monitor；
6. 运行双方签名快照请求并核对 Admin route hash。

单节点操作使用部署目录脚本：

```bash
./stop.sh monitor
./restart.sh gateway
./healthcheck.sh gateway
./start.sh monitor
```

Admin 故障期间不要删除 `data/gateway/routes.json`；Gateway 会继续使用缓存。无缓存的新节点
必须先恢复 Admin，首次拉取成功后才能 ready。

## 对端故障演练

默认 `pull_interval_seconds=10`、`timeout_seconds=5`，连续超过 `3 * timeout` 未收到对端
快照后实例变为 `down` 并写入 `monitor-peer/<instance_id>` 的 triggered/firing 告警；恢复
拉取后写入 resolved 告警。

1. 先运行双方签名快照请求，确认两端都能看到对端实例为 `active`。
2. 在香港执行 `./stop.sh monitor`，保持 Gateway 运行。
3. 等待 25 秒，在广州服务监控的实例和告警事件中确认 `monitor-hk-177` 为 `down`，并出现
   `monitor-peer/monitor-hk-177` 的 `triggered/firing` 事件。
4. 在香港执行 `./start.sh monitor`，等待 15 秒；确认实例恢复为 `active` 且出现
   `resolved` 事件。
5. 反向停止广州 Monitor，重复验证香港侧。
6. 最后再次执行双方签名快照请求和端口不可达检查。

本地自动化对应命令：

```bash
go test -count=1 ./modules/gateway/test ./modules/admin/test ./modules/monitor/test
```

远端验收记录应包含时间、操作节点、双方 route hash、故障/恢复事件 ID，以及端口检查
结果。旧 `moox-monitor-tunnel.service`、反向隧道脚本和环境文件必须在新链路验收完成后删除，
删除后完整重做本节演练。
