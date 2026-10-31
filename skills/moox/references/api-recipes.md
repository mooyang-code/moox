# 管理台操作与命令对照（专用命令优先，gateway call 兜底）

管理台上的每个操作都有对应的 `moox-cli` 专用命令（下面按页面列出）。专用命令把参数映射到请求字段，帮你省去拼 JSON，并对破坏性操作默认只预览。
只有底层接口（Storage 数据面）没有专用命令，才用 `moox-cli gateway call` 直接调用内部服务（见文末）。
交易模块尚未完工：只能查询，不代用户创建账户、下单或清仓。

## 通用约定

- 命令在产物包（或仓库）根目录执行，读取当前目录的 `moox.toml`（`--file` 指定），经里面的 SSH 配置连接各台主机，不需要单独登录、建隧道或设置环境变量。
- 每个命令的 `--help` 写明对应的服务与方法。**字段名就是 proto 字段名（`snake_case`）**；字段多的写操作用 `--data @文件` 给出完整请求（`-` 读标准输入），各参数优先于 `--data`。
- 输出是 JSON；`ret_info.code` 非 0 时先打印响应、再以非 0 退出。分页命令用 `--page`、`--page-size`（默认 1、50）。
- **破坏性命令默认只预览**：不带 `--yes` 输出 `preview: true` 的 JSON（将要发出的请求），**把预览给用户看，确认后再加 `--yes`**。
- 带 DSL、长文本的参数用文件：`--spec-file`、`--dsl-file`、`--template-file`（`-` 读标准输入），不要在命令行里拼多行 YAML。
- 带 `--space-id` 的命令默认空间是 `crypto`；策略与云函数服务按请求头里的空间取上下文，这些命令也都带 `--space-id`。

## 对照表

| 管理台 | 用途 | 命令 |
| --- | --- | --- |
| 设置 › 空间 | 列出、查看、新建、修改、删除 | `space list`、`space show --space-id`、`space create|update --space-id … --name …`、`space delete --space-id`（预览） |
| 设置 › 密钥 | 看清单（值已打码） | `secret list`、`secret show --secret-id`；新建、修改请用户在管理台填写 |
| 数据 › 来源 | 数据来源 | `metadata source list|show|create|update|delete` |
| 数据 › 标的 | 标的、标签与成员 | `metadata subject list|show|upsert|resolve`、`metadata tag list|show|upsert|delete|list-members|member-status`、`metadata tag members|clone` |
| 数据 › 字段 | 字段与分组 | `metadata field list|show|create|update|batch-update`、`metadata field-group list|show|create|update|delete` |
| 数据 › 数据集、View | 数据集、列、View | `metadata dataset list|show|columns|subjects|activation|create|update|add-column|activate|rebind|delete`、`metadata view list|show|columns|create|update|add-column|delete`、`metadata device list|show|create|update` |
| 数据 › 浏览、导入 | 读数据、导入历史 | `data kline get`、`data rows export|count`、`data view query`、`data records search`、`import` |
| 采集 › 采集任务 | 任务、结果清单、数据类型 | `collector task list|show|create|update|disable|delete|delete-result|inventory`、`collector data-type list|show` |
| 采集 › 运行状态 | 状态、缺口、补采、合成、标的、采集方式 | `collector status`、`collector kline …`、`collector resample preview|backfill|jobs|cancel`、`collector subjects status|listers|sync|exclude|unexclude|exclusions`、`collector ingest-mode get|set`、`collector fetch-plan …` |
| 采集 › 云账户、云函数、代码包 | 账户（只读与删除）、实例、作业、导入、代码包 | `cloud account list|regions|delete`、`cloud function list|job|invoke|create|deploy|update-config|delete`、`cloud scf preview|import`、`cloud package list|show|url|delete`、`collector function upload` |
| 因子 › 因子定义 | 定义、分类、校验与试算 | `factor list|show|create|update|delete|validate|preview|functions`、`factor category list|upsert|delete|assign` |
| 因子 › 计算任务 | 任务、成员、补算、状态 | `factor task list|show|create|enable|disable|delete|params`、`factor member add|remove|enable|disable`、`factor recalc submit|list|show|cancel`、`factor status`；阶段 `lifecycle …`；自定义算子 `udf …` |
| 策略 | 定义、实例、结果、回放、模板 | `strategy list|show|validate|create|update|delete`、`strategy instance list|show|create|update|enable|disable`、`strategy result list|show`、`strategy target list`、`strategy replay start|list|show|bars|cancel`、`strategy template list|show|save|delete|render`、`strategy display get|set` |
| 交易（只读） | 账户、订单、成交、持仓、权益 | `trade account list|show`、`trade logical list|show|target`、`trade order list|show`、`trade fills|positions|holdings|equity|capabilities|action` |
| 运维 › 监控 | 总览、到达历史、主机资源、告警屏蔽 | `monitor health|arrival|hosts|host-metrics|mute|unmute|mute-status`、`doctor alerts`、`eventbus overview|streams|consumers|consumer|events` |
| 运维 › 主机、部署 | 查询；增删启停只走 setup | `host list|deployments|routes|catalog|directory`、`host agent status|snapshot|run-once --host`、`setup hosts|host|deployment|remove-host|sync-deployments …` |
| 运维 › 存储节点 | 登记查询与修改 | `storage node list|show|update|delete` |
| 运维 › 活动日志 | 活动日志 | `activity list` |

常用写法：

```bash
# 因子：校验表达式、创建定义、加成员、启用、补算
./bin/moox-cli factor validate --spec-file my.yaml
./bin/moox-cli factor create --factor-id ma --name 均线 --spec-file my.yaml
./bin/moox-cli factor member add --task-id <任务> --factor-id ma --param window=20
./bin/moox-cli factor member enable --task-id <任务> --member-key ma__20
./bin/moox-cli factor recalc submit --task-id <任务> --from 2026-10-01T00:00:00Z --to 2026-10-02T00:00:00Z

# 策略：校验 DSL、创建定义与实例（实例新建后停用，确认无误再启用）
./bin/moox-cli strategy validate --dsl-file s.yaml --factor-task-id <任务>
./bin/moox-cli strategy create --dsl-file s.yaml
./bin/moox-cli strategy instance create --strategy-id <ID> --factor-task-id <任务>
./bin/moox-cli strategy instance enable --instance-id <ID>        # 先预览，确认后加 --yes

# 破坏性：先预览，给用户确认后再加 --yes
./bin/moox-cli factor task delete --task-id <任务>
./bin/moox-cli factor task delete --task-id <任务> --yes
```

## gateway call（兜底）

没有专用命令的底层接口，用 `gateway call` 直接调用。目前只剩 Storage 数据面（`trpc.moox.storage.PrimaryStore` 的 `ReadRows`、`ReadDatasetChanges`、`CountTimeSeriesRows` 等底层读写）：

```bash
# 列出可调用的服务与方法（读内嵌服务目录，不连网），可带关键字过滤；会标出只读/写、moox-cli 是否有权调用、是否被 gateway call 禁用
./bin/moox-cli gateway methods
./bin/moox-cli gateway methods storage

# 调用：服务写完整名称；--data 是 JSON，@文件 读文件，- 读标准输入；--space-id 是请求携带的空间（默认 crypto）；--host 只调用指定主机上的服务
./bin/moox-cli gateway call trpc.moox.storage.PrimaryStore ReadRows --space-id crypto --data @请求.json
```

- 业务失败（`ret_info.code` 非 0，`0` 或 `SUCCESS` 才算成功）时先打印响应、再以非 0 退出。
- **写方法不带 `--yes` 只预览**（输出将要发出的请求）：把预览给用户看，确认后再加 `--yes`；`gateway methods` 标为“写”的都算，只读方法不受影响。
- **`gateway call` 不能调用返回密钥明文或完整 webhook 的方法**（`SecretMgr.GetSecretValue`、`MonitorMgr.ListNotificationChannels`），也拒绝密钥的新建、修改、启停、删除（`CreateSecret`、`UpdateSecret`、`ToggleSecretStatus`、`DeleteSecret`，由用户在管理台操作）：云账户凭证由专用命令在进程内使用，告警渠道用 `config notification` 查看（地址已打码）。
- 请求字段以 `modules/<模块>/proto/*.proto` 为准；分页字段因服务而异（策略用 `{"page":{"page":1,"page_size":50}}`，其余用 `{"page":{"page":1,"size":50}}`）。

## 密钥（trpc.moox.ops.SecretMgr）

只用 `secret list` / `secret show` 查看有哪些密钥（不返回明文）。新建、修改密钥会让明文经过 Agent，**不要代用户写入**：请用户在管理台“设置 › 密钥”页自己填写。
