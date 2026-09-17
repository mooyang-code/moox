# MooX 系统初始化

## 前置条件

1. 从 `moox.toml.example` 创建根目录 `moox.toml`，文件由当前用户持有且权限为
   `0600`。
2. 完成控制面部署和 `setup apply`。
3. 使用 `setup deploy-storage` 把 Storage 部署到 `moox.toml` 中的一台主机。

`setup deploy-control` 会一起安装受管 Caddy、选择公网 ACME 或 internal CA、验收
HTTPS，并配置健康检查以维持 Caddy 运行和自动续期。公网 IP/DNS 不需要用户安装根
证书；私网或回环地址才需要通过 `caddy-ca.sh` 分发 internal CA。

## 默认配置

默认配置集中在 [`config/setup/`](../config/setup/)：

| 文件 | 读取方 | 用途 |
| --- | --- | --- |
| `metadata.yaml` | `moox-cli setup init` | Admin 业务空间和 Storage 元数据 |
| `dataset-health-policy.yaml` | Monitor | 实时 Dataset 健康策略 |
| `service-deployments.yaml` | Admin CLI | 默认服务部署清单 |
| `collector-rules.yaml` | Collector CLI | 默认行情采集规则 |

`setup init` 只读取固定文件名 `metadata.yaml`，不会扫描或合并目录中的其他 YAML；
其他模块各自读取对应的职责专属文件。
业务空间为 `stockcn` 和 `crypto`；`attributes.scope=internal` 的 `mooxsys`
只进入 Storage。

腾讯云 SCF 使用根目录 `moox.toml` 中唯一的 `[scf_fetcher.cloud_account]` 配置。
其中只引用 Admin/SecretMgr 中的 `credential_secret_id`，并配置统一的 COS 区域和存储桶；
SCF 的各个 `regions` 只描述函数部署地域，不再为每个地域复制云账户。

同一文件的 `[scf_fetcher.tencent_limits]` 保存腾讯云默认配额参考：每地域 5 个命名空间、
每命名空间 50 个函数、每分钟默认 burst 500，以及已核对地域的账号级并发内存配额。
发布校验使用 `max_functions_per_namespace` 作为每个 SCF 地域的函数数上限；这是默认配额
而不是实时账号状态。购买套餐、工单提额或腾讯云调整后，应同步更新该配置，并在发布前通过
腾讯云控制台或 API 再核对。未出现在 `total_concurrency_memory_mb_by_region` 的地域不作
并发内存假设，不能仅凭配置推断其可用节点数。每个 Space 建议使用独立 `namespace`；校验
还会为每个活跃地域预留 1 个 Invoke canary，并为 `stockcn` 快照地域预留 1 个 Instrument
Timer，均计入同一 namespace 的函数配额。首次发布非 `default` namespace 时，CloudNode
会在目标地域先幂等创建命名空间；已有同名函数但属于其他 namespace 的节点不会被复用。
当前仓库的 `moox.toml` 为避免误迁移仍保留既有 `default` fleet 且默认关闭 SCF；启用前若
同一地域的多个 Space 合计超过 50（含辅助函数），应先显式迁移到专用 namespace 或减少地域
节点数，发布校验会拒绝超额配置。

## 初始化命令

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host storage
```

`--storage-host` 是已部署 Storage 的主机名，不是 IP 地址。命令按以下顺序执行：

1. 幂等打开所有已配置运行主机需要的腾讯云入站端口；若有目标无法解析到云实例，初始化会以 `firewall_incomplete` 停止，并报告跳过的目标。
2. 严格解析并校验 Metadata 依赖。
3. 在 Admin 的同一事务中创建或核对管理员、凭据、主机和业务空间。
4. 检查 Admin 状态并验证管理台登录。
5. 在 CloudNode 中创建或逐字段核对这一条 Tencent CloudAccount。
6. 通过 Storage 主机 SSH 隧道创建或逐字段核对元数据。
7. 对每个 Dataset 执行就绪检查和 revision CAS 激活。
8. 再次核对全部 Storage 元数据并输出脱敏 JSON 汇总。

若启用了 `[scf_fetcher]`，初始化开始时还会只读发现 `storage_host` 对应的腾讯云实例，
把地域、可用区、VPC、子网和私网地址写入返回的 `scf_routes` 计划。该计划不修改云资源，
后续 `collector function publish submit` 会优先完整发布 Storage 同地域配置的节点数，并为
同地域函数设置 `ip://<storage-private-ip>:11003` 和 Storage 的 VPC/子网；该地域的
Invoke canary、Timer 批次和回读全部成功后才继续其他地域。其他地域使用
`ip://<storage-public-ip>:11003`。跨地域不创建 CCN。可单独查看计划：
这里的“完整发布”按该地域 manifest 的 `function_count` 计算；CLI 不会自动迁移其他地域的
节点配额。

```bash
moox-cli setup scf-network-plan --file ./moox.toml
```

SCF 函数需要访问交易所公网时，继续将 `public_net_status = "ENABLE"`；这与访问 Storage
的私网/公网路径是两个独立开关。初始化或发布前先完成同地域 Invoke canary，验证 SCF 日志
中的 Storage 写入和 Provider HTTP 200，再放大 Timer fleet。CLI 不修改 `moox.toml`。

命令可重复执行。相同资源记为 `unchanged`；已激活且锁定绑定的 Dataset 也记为
`unchanged`。同 ID 资源字段不一致时返回冲突，不自动覆盖现有配置或数据。

完成后登录管理台，在空间选择器中可切换“A股市场”和“加密货币市场”，并查看各自的
Dataset、Field、DatasetColumn 和 View。

## 局部导入

`metadata spaces` 和 `setup metadata-import` 保留给局部导入或诊断场景。新系统的
标准初始化应优先使用 `setup init`，避免手工选择多个 YAML 或漏掉 Admin 空间。
