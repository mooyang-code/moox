# 系统初始化配置

这个目录是发布包和 `moox-cli setup init --config-dir` 使用的版本化初始化种子，包含四个固定种子文件：

| 文件 | 消费方 | 内容 |
| --- | --- | --- |
| `metadata.yaml` | `moox-cli setup init` | Admin 业务 Space 与 Storage 元数据 |
| `dataset-health-policy.yaml` | Monitor | 实时 Dataset 健康阈值 |
| `collection-tasks.yaml` | Collector | 默认采集任务 |
| `factor-tasks.yaml` | `moox-cli setup start` | 默认因子定义与因子任务 |

`metadata.yaml` 的 Space 包含 `stockcn`、`stockhk`、`stockus` 和 `crypto`，都是业务空间；主机快照存在 Monitor 自己的库里，Storage 没有系统空间。

SCF 采集函数的云账号、地域网络与空间在根目录 `moox.toml` 的 `[scf_plan]` 声明；函数数量由 Collector 按标的数与每函数标的数自动计算，每个空间使用独立 namespace。
发布校验会拒绝包含辅助函数后超过配额的配置。

初始化可重复执行。已有资源与声明一致时报告 unchanged；契约不一致时失败，不覆盖已有
配置和数据。Dataset 建好即为 active，可以直接写入，
不需要另外激活。

需要清空全部行情与因子数据时，按 `docs/部署与运维.md`「清空全部数据重新积累」的顺序操作；
数据集、View、标的等定义保留，不需要重新导入本目录。

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host storage
```

`--storage-host` 必须是 `moox.toml` 中已经部署 Storage 的主机名。`setup init` 读取
`metadata.yaml` 创建 Admin 业务空间并完成 Storage 元数据初始化；Monitor 的
`dataset-health-policy.yaml` 和 Collector 的 `collection-tasks.yaml` 由打包与原生安装流程放入各组件配置目录，分别在模块启动时读取。

启用 SCF 时，每个采集地域都要有一台同地域、带私网地址、写了 `external_entry = true` 的入口主机；函数在该地域 VPC 里经
它的外部入口私网地址写入，规则见 `skills/moox/references/private-network.md`。

默认因子任务在 `factor-tasks.yaml`：`tasks` 声明因子任务（`id`、`name`、`space_id`、`source_dataset_id`、`freq`），
任务的成员用内嵌的 `members` 写：`{factor: ema, params: {span: 20, warmup: 50000}}` 是固定成员，`{factor: ma, var: true}` 是可变成员（由关联的策略逐期提交参数）；
`definitions` 声明因子定义（`factor_id` 加因子库文件，路径相对 `source_dir`，`source_dir` 相对这个文件所在的目录）。
`setup start --space-id <空间>` 导入这个空间的任务（定义全部导入），没有因子机时跳过。
自己的任务写成同样格式的 yaml，用 `moox-cli factor task apply --file <文件>` 导入。两者都只建不改：先只读核对线上已有的分类、定义、任务和成员，
线上已有且内容不同时逐项列出差异、一项都不写，退出码非 0；内容相同算已存在；线上任务里多出来的成员只报告、不移除。
核对通过后的写入不是一个事务：写到一半失败（网络断开、成员参数被 FactorMgr 拒绝等）会留下已建好的部分，修好原因后重跑同一条命令，已建的算已存在，接着补齐。
中断在“成员已加入、还没按文件启用”之间（新成员先以停用状态加入）时：
- `setup start` 重跑会把这种“线上停用、文件启用、参数一致”的成员当作上次没启用完，接着启用，安装能继续；
- 用户手动执行的 `factor task apply` 仍把它报成“状态不同”、一项都不写（不会把在管理台里手工停用的成员重新启用），确认后用 `moox-cli factor member enable` 启用再重跑。

告警渠道的初始值也只建不改：Monitor 一个渠道都没有时才写入秘密文件里的 `[notification]`。Monitor 删除渠道是硬删除，
所以在管理台把渠道全部删掉之后再重跑写渠道的那一步（`setup install` 或 `setup init`），会把秘密文件里的旧渠道写回去；
不想要它就先把秘密文件里的 `webhook_url` 清空。
