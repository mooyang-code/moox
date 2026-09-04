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

## 初始化命令

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host storage
```

`--storage-host` 是已部署 Storage 的主机名，不是 IP 地址。命令按以下顺序执行：

1. 严格解析并校验 Metadata 依赖。
2. 在 Admin 的同一事务中创建或核对管理员、凭据、主机和业务空间。
3. 检查 Admin 状态并验证管理台登录。
4. 在 CloudNode 中创建或逐字段核对这一条 Tencent CloudAccount。
5. 通过 Storage 主机 SSH 隧道创建或逐字段核对元数据。
6. 对每个 Dataset 执行就绪检查和 revision CAS 激活。
7. 再次核对全部 Storage 元数据并输出脱敏 JSON 汇总。

命令可重复执行。相同资源记为 `unchanged`；已激活且锁定绑定的 Dataset 也记为
`unchanged`。同 ID 资源字段不一致时返回冲突，不自动覆盖现有配置或数据。

完成后登录管理台，在空间选择器中可切换“A股市场”和“加密货币市场”，并查看各自的
Dataset、Field、DatasetColumn 和 View。

## 局部导入

`metadata spaces` 和 `setup metadata-import` 保留给局部导入或诊断场景。新系统的
标准初始化应优先使用 `setup init`，避免手工选择多个 YAML 或漏掉 Admin 空间。
