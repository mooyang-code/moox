# MooX 系统初始化

## 前置条件

1. 从 `custom.toml.example` 创建根目录 `custom.toml`，文件由当前用户持有且权限为
   `0600`。
2. 完成控制面部署和 `setup apply`。
3. 使用 `setup deploy-storage` 把 Storage 部署到 `custom.toml` 中的一台主机。

## 默认配置

默认配置集中在 [`examples/setup/default/`](../examples/setup/default/)：

| 文件 | 读取方 | 用途 |
| --- | --- | --- |
| `metadata.yaml` | `moox-cli setup init` | Admin 业务空间和 Storage 元数据 |
| `dataset-health-policy.yaml` | Monitor | 实时 Dataset 健康策略 |
| `service-deployments.yaml` | Admin CLI | 默认服务部署清单 |

`setup init` 只读取固定文件名 `metadata.yaml`，不会扫描或合并目录中的其他 YAML。
业务空间为 `stock_cn` 和 `crypto`；`attributes.scope=internal` 的 `moox_system`
只进入 Storage。

## 初始化命令

```bash
moox-cli setup init \
  --file ./custom.toml \
  --config-dir ./examples/setup/default \
  --storage-host control
```

`--storage-host` 是已部署 Storage 的主机名，不是 IP 地址。命令按以下顺序执行：

1. 严格解析并校验 Metadata 依赖。
2. 在 Admin 的同一事务中创建或核对管理员、凭据、主机和业务空间。
3. 检查 Admin 状态并验证管理台登录。
4. 通过 Storage 主机 SSH 隧道创建或逐字段核对元数据。
5. 对每个 Dataset 执行就绪检查和 revision CAS 激活。
6. 再次核对全部 Storage 元数据并输出脱敏 JSON 汇总。

命令可重复执行。相同资源记为 `unchanged`；已激活且锁定绑定的 Dataset 也记为
`unchanged`。同 ID 资源字段不一致时返回冲突，不自动覆盖现有配置或数据。

完成后登录管理台，在空间选择器中可切换“A股市场”和“加密货币市场”，并查看各自的
Dataset、Field、DatasetColumn 和 View。

## 局部导入

`metadata spaces` 和 `setup metadata-import` 保留给局部导入或诊断场景。新系统的
标准初始化应优先使用 `setup init`，避免手工选择多个 YAML 或漏掉 Admin 空间。
