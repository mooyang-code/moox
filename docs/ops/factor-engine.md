# 因子计算引擎运维手册

> 对应设计：[因子模块拆分设计](../superpowers/specs/2026-10-05-factor-manager-engine-split-design.md)。
> 管理端 `moox-factor-mgr` 随控制机部署（`deploy-moox.sh --profile control`）；计算引擎
> `moox-factor-engine` 部署在操作员本机，只做出站连接。

## 拓扑与端口

| 方向 | 地址 | 用途 |
| --- | --- | --- |
| 引擎 → 控制机 | `https://<控制机>:11001/api/service/factormgr/*` | 同步目录、心跳、领取 / 上报补算任务 |
| 引擎 → 控制机 | `tls://<控制机>:4222` | 订阅 `CollectorPeriodCompleted` |
| 引擎 → Storage 机 | `ip://<Storage 机>:11004` | storage-access：读源数据、写因子结果与周期标记 |

引擎本机端口：health `11417`、admin `11945`、metrics `12945`，均只监听 loopback。

## 凭据清单

放在安装目录 `secrets/` 下，必须是 0600 的普通文件：

| 文件 | 来源 | 说明 |
| --- | --- | --- |
| `gateway-factor-engine.key` | 控制机 `secrets/gateway-factor-engine.key` | 网关调用方 `factor-engine`，只能调 FactorEngine 的 4 个方法 |
| `storage-access-factor-engine.key` | Storage 机 `secrets/storage-access-factor-engine.key` | storage-access 主体 `factor-engine` 的入站密钥 |
| `storage-primary-auth.secret` | Storage 部署的 `MOOX_STORAGE_PRIMARY_AUTH_SECRET` | 为 Storage 请求里的 AppId `moox-factor` 签名 |
| `factor-eventbus.yaml` | 控制机 `~/.config/moox/eventbus/factor-eventbus.yaml` | EventBus 角色凭据；`ca_file` 相对本文件，一并拷贝 |
| `control-caddy-root.crt` | 控制机 Caddy 根证书（`--manager-ca` 拷入） | 校验 `:11001` 的 TLS |
| `health-auth.env` | 安装脚本首次生成 | `/readyz` 签名密钥 |

Storage 机的 `storage-access-factor-engine.key` 与网关服务密钥一样由 gateway service secret 派生，
每次发布保持不变；也可用 `MOOX_STORAGE_ACCESS_FACTOR_ENGINE_SECRET_FILE` 显式指定。

## 安装与更新

```bash
scripts/deploy/deploy-factor-engine.sh \
  --dir ~/Documents/moox-deploy \
  --manager-url https://<控制机>:11001 --manager-node-id <控制机网关节点> \
  --manager-ca /path/to/control-caddy-root.crt \
  --storage-target ip://<Storage 机>:11004 --storage-node-id storage-access-<storage 节点> \
  --eventbus-url tls://<控制机>:4222
```

脚本会编译引擎（Go 构建绕过本机 HTTP 代理）、拷贝 Python worker、准备含 pandas / numpy 的
`venv`、渲染 `config/engine.yaml`，macOS 用 launchd（`~/Library/LaunchAgents/com.moox.factor-engine.plist`）、
Linux 用 systemd 用户服务托管，并等待 `/readyz`。服务环境里不带任何 `HTTP(S)_PROXY`；引擎访问管理端的
HTTP 客户端本身也忽略代理环境变量。

## 启停与检查

```bash
launchctl print gui/$(id -u)/com.moox.factor-engine      # macOS 状态
launchctl kickstart -k gui/$(id -u)/com.moox.factor-engine # 重启
launchctl bootout gui/$(id -u)/com.moox.factor-engine      # 停止
set -a; source ~/Documents/moox-deploy/secrets/health-auth.env; set +a
~/Documents/moox-deploy/bin/moox-factor-engine health      # 打印 /readyz 详情
tail -f ~/Documents/moox-deploy/logs/factor-engine.log
```

管理端视角：前端「因子总览」的计算引擎卡片，或 `FactorMgr.GetStatus` 的 `engine` 字段
（`online`、`last_heartbeat_at`、`catalog_in_sync`）。

## 排障

| 现象 | 原因与处理 |
| --- | --- |
| `/readyz` 503，`lease_conflict=true` | 另一个 `engine_id` 持有租约（45 秒内有心跳）。停掉多余的引擎；旧引擎心跳停止 45 秒后新引擎自动接管 |
| 日志 `factor_catalog_sync_failed`，`catalog_sync_failing_since` 不为空 | 管理端不可达。引擎继续用最后快照计算；恢复后下一次整分钟第 45 秒自动追平 |
| 前端提示「引擎目录未同步」 | 管理端目录已变化而引擎尚未拉取，最长约 1 分钟；持续存在时查上一行 |
| 新启用的成员前 1～2 个周期没有结果 | 设计如此：启用到引擎下次同步之间的周期不自动补算，需要时手动提交补算 |
| 事件反复 `factor_period_retry` | 结果数据集未就绪（管理端对账失败）或 storage-access 不可达；查管理端日志与 `nc -zv <Storage 机> 11004` |
| 补算任务 `running` 但不前进 | 执行它的引擎已停止；任务租约（默认 15 分钟）过期后会被重新领取并从 `progress_time` 续跑 |
| Go 构建或模块下载极慢 | 本机 `HTTPS_PROXY` 拖慢 goproxy.cn；安装脚本已绕开代理，手动构建时先 `unset HTTPS_PROXY HTTP_PROXY` |
