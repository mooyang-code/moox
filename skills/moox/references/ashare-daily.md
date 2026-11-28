# 打开 A 股日线附加数据（复权、申万行业、财务、换手率、股本）

A 股日线任务可以在每根日线上带五组附加字段，共 27 个：后复权因子（`adj_factor`）、申万行业（`sw_l1`、`sw_l2`、`sw_l3`）、财务（`fin_` 开头的 18 个字段）、换手率（`turnover_rate`，百分比）、股本（`total_shares`、`float_shares`、`dividend_per_share`、`is_st`）。
它们是日线数据集里的列，经日线因子任务进入因子结果，日内策略写 `sources` 后用 `daily.列名` 读取。
字段含义、取值口径和限制见 [`docs/模块/采集.md`](../../../docs/模块/采集.md) 第 4a、4b 节。

动手前先跟用户说清这几件事：

- **输出字段只能在建任务时选全。** 任务建好后只能减少输出字段，不能新增；要加字段只能新建采集任务和对应的因子任务。所以带附加字段的内置任务一次带齐 49 个字段（22 个基础字段加 27 个附加字段）。
- **两条内置日线任务，按需要选。** “A 股日线（复权、行业、财务、换手率、股本）”（`dasftksvjhj2jom4vhj0`）带全部附加字段，任一附加来源当天报错，这只股票当天整行不写；“A 股 K 线 1d”（`dasftksvjhj2jom4vhi0`）只有行情，不受附加来源影响。只要日线行情时用后者，下面的命令把任务 ID 换掉即可。两条都默认停用，`setup start` 不会启用它们。
- **日线在北京时间次日 08:00 才采集。** 交易日 D 的日线在 D+1 日 08:00 到。用它做来源的日内策略，D 日 15:00 收盘那一根的结果在次日开盘前给出。
- **内置任务只覆盖沪深 300（标签 `cn_hs300`）。** 自建日线任务（2b）可以绑别的 A 股股票标签，例如 `cn_a_share`（沪深北全部股票），建任务时不按标签限制附加字段。五组附加字段只对沪深两个交易所的股票取数；北交所股票照常有日线行情，这些列为空，用到它们的市值、估值、筹码因子也为空。
- **换手率有完整历史，股本这一组只有开通以后的值。** 股本、每股股利、是否 ST 来自只有当日值的接口，补历史时这些列是空的（只有最近一个交易日可能写上）；所以市值、市盈率这类因子只能从开通日起用。平日某天没采到，那一天的股本也补不回来（内置的市值、估值因子会沿用最近 20 个交易日里的值）。
- **沪深 300 里没有 ST 股**，`is_st` 在这个股票池里基本恒为 0。

全部命令在产物包目录（或源码仓库根目录）执行，默认读当前目录的 `moox.toml`。`--space-id` 的默认值是 `crypto`（`collector kline`、`collector status`、`collector fetch-plan`、`collector task create/delete` 没有默认值，必须写），下面带这个参数的命令（`collector`、`factor`、`strategy`）都要写 `--space-id stockcn`；`metadata apply`（用 `--spaces`）、`host`、`setup restart` 没有这个参数。

## 0. 前提

- stockcn 空间走云函数采集，`moox.toml` 的 `[scf_plan]` 里要有这个空间。
- 确认 5m 的 A 股采集已经正常（`./bin/moox-cli collector status --space-id stockcn`），再打开日线。

## 1. 确认可选的附加字段

可采哪些字段、它们的中文名与分组只在 Collector 的字段规格里登记，管理台新建任务的输出字段清单与数据源页都直接取自它，不需要导入字段目录。先看日线能选哪些附加字段：

```bash
./bin/moox-cli collector data-type show --data-type kline
```

输出的 `extra_fields` 里 `market_id=stockcn`、`frequencies` 含 `1d` 的条目应有 `adj_factor`、`sw_l1`～`sw_l3`、`fin_*`、`turnover_rate`、`total_shares`、`float_shares`、`dividend_per_share`、`is_st`，每个都带 `display_name` 与 `group`。结果数据集建列时 Collector 把显示名与分组写在列上，管理台按列自己的显示名与分组展示。

## 2. 打开采集任务

### 2a. 全新安装：启用内置任务

全新安装（Collector 任务表为空时）会写入两条内置日线任务，默认停用：“A 股日线（复权、行业、财务、换手率、股本）”`dasftksvjhj2jom4vhj0` 和“A 股 K 线 1d”`dasftksvjhj2jom4vhi0`。

```bash
./bin/moox-cli collector task list --space-id stockcn                                          # 找到任务，看 enabled
./bin/moox-cli collector task show --space-id stockcn --task-id dasftksvjhj2jom4vhj0           # 核对 frequency=1d 和 49 个 output_fields
./bin/moox-cli collector task update --space-id stockcn --task-id dasftksvjhj2jom4vhj0 --enabled=true
```

- `collector task update` 没有预览，执行即生效，不需要也不接受 `--yes`；它先取回现有任务再合并，只改启用状态。执行前先跟用户确认。
- 写成 `--enabled=true`（带等号）。停用用 `collector task disable --space-id stockcn --task-id <ID>`，不带 `--yes` 只预览。
- `./bin/moox-cli setup start --space-id stockcn` 不启用这两条任务（种子里标了 `start: manual`），只在输出的 `notes` 里提示“需手动启用”。必须用上面的 `task update` 显式启用。

### 2b. 已有部署：补写内置任务再启用

内置任务只在任务表为空时自动写入，已有部署里不会自动出现。`task list` 里没有它们时，按下面的顺序做，每一步改线上数据前先给用户看命令：

1. **补写内置任务。** 在控制机上执行（`<控制机>` 是 `moox.toml` 里控制机的主机 ID；路径按默认部署根 `/data/moox`，先 `ls` 确认文件存在）：

   ```bash
   ./bin/moox-cli host exec --host <控制机> -- /data/moox/bin/moox-collector-cli init \
     --db-path /data/moox/data/collector/moox_collector.db \
     --seed-file /data/moox/config/collector/collection-tasks.yaml
   ```

   - 它只新建库里没有的（空间, 任务 ID），已有任务（包括用户改过、停用的）不动；输出里 `tasks_created` 是补写的个数，`tasks_unchanged` 是已存在的个数。同时会执行一次建表语句（`CREATE TABLE IF NOT EXISTS`，对已有的表没有影响）。
   - 种子里的其他内置任务如果用户以前删过，也会被补回来（停用状态）。执行前用 `task list` 对一下，补回来又不需要的再用 `collector task delete` 删（默认只预览，`--yes` 才执行；默认连结果数据集一起删，要保留结果加 `--keep-result`）。
2. **重启 Collector。** 这样直接写进任务表的任务，Collector 要到启动时才给它建结果数据集（查询索引随之自动建）：

   ```bash
   ./bin/moox-cli setup restart --host <控制机> --component collector          # 预览
   ./bin/moox-cli setup restart --host <控制机> --component collector --yes
   ```

3. **启用。** 用 2a 的 `collector task update --enabled=true`。

不要改用 `collector task create` 新建同名任务：新建的任务 ID 是随机的，和内置任务的固定 ID 不同，结果数据集 ID 也就不同，文档、因子配置里按固定 ID 写的地方都对不上。只有需要“字段组合与内置任务不同”的日线任务时才新建（`--output-field` 会替换默认字段，要的字段列全；这个参数可以重复写，每次用逗号分隔）：

```bash
./bin/moox-cli collector task create --space-id stockcn \
  --name "A 股日线（复权、行业）" \
  --tag-id cn_hs300 --frequency 1d \
  --output-field open,high,low,close,volume,amount \
  --output-field instrument_name,trade_date,close_time,volume_unit,amount_unit,provider_symbol,provider_timestamp,fetched_at \
  --output-field request_id,route_id,route_rank,source_provider,quality_status,amount_quality,provider_id,source_id \
  --output-field adj_factor,sw_l1,sw_l2,sw_l3
```

- 这条命令没有预览，执行就创建并启用（想先停用加 `--disabled`）。执行前把命令给用户确认。
- 输出里的 `task_id` 记下来。结果数据集的 ID 是 `dataset_<task_id>`（内置任务是 `dataset_dasftksvjhj2jom4vhj0`、`dataset_dasftksvjhj2jom4vhi0`），用 `collector task show` 或 `collector task inventory --space-id stockcn` 核对。
- 创建之后字段加不回来。
- 报“输出字段 … 在标签 … 的市场 … 下没有来源”或“只支持 1d 频率”时，说明标签不是 A 股股票或频率不是 `1d`，按报错改。

### 2c. 等采集计划生效

启用、停用日线任务在下一轮对账（1 分钟内）生成新的采集计划，几分钟内生效；日线的新份从下一个北京时间 08:00 起采。
同一空间里别的数据集（例如 5m）的变更不受日线影响，照常几分钟内生效。

```bash
./bin/moox-cli collector status --space-id stockcn         # 数据集出现在列表里、level 为 ok
./bin/moox-cli collector fetch-plan --space-id stockcn     # 计划已生效，函数分配里有这个数据集
```

实时采集从计划生效之后的第一个交易日起：那天的日线在下一天北京时间 08:00 到。

## 3. 补历史

计划生效之前的历史用强制补采。日线的行键是交易日期的 UTC 零点，`--from`、`--to` 直接写日期（左闭右开）：

```bash
./bin/moox-cli collector kline backfill --space-id stockcn --dataset-id dataset_<task_id> \
  --from 2025-11-01 --to 2026-11-11 --force
./bin/moox-cli collector kline backfill-status --space-id stockcn --dataset-id dataset_<task_id>      # 看作业进度
./bin/moox-cli collector kline backfill-status --space-id stockcn --job-id <job_id>                   # 看单个作业与失败分块
./bin/moox-cli collector kline backfill-retry --space-id stockcn --job-id <job_id>                    # 重试失败分块
```

- `--force` 是必须的：不带时只补采集计划覆盖的时间里的缺口，计划生效之前的日期不会补。带 `--force` 是把范围内应有的日线全部重拉重写。
- 命令没有预览，执行即提交作业。一个分块是一只股票至多 1000 天的日线（补一年、两年都是每只一个分块），沪深 300 补一年约 300 个分块。
- 补采时每只股票都要取复权因子、三张财务报表、换手率、股本，每个分块下载一次申万行业文件；任何一个来源报错，这个分块失败，用 `backfill-retry` 重试。
- 要用筹码类因子（默认回看 210 个交易日）时补两年：只补一年的话，最近一年里只有最后一个多月有值。
- 通达信的日线最多回看 10 年（3650 天），起点更早的分块改由东方财富取；超过约 8 年的补采没有验证过，先补一小段试。
- 财务数据重取会变：较早的报告期，重取得到的是后来重新列示的数值。已经有实时数据的区间不要随手 `--force` 重拉。

## 4. 建日线因子任务

策略读的是因子任务的结果，所以要给日线数据集建一个 `1d` 因子任务。日线数据集的全部列（包括 27 个附加字段）会原样带进因子结果，行业和财务字段不用另写因子就能在策略里读。
但任务里至少要有一个启用的成员：没有成员的任务不计算、结果数据集是空的，也不能绑给策略实例。只想读行业和财务字段时，加下面任意一个因子即可。

```bash
./bin/moox-cli factor task create --space-id stockcn --source-dataset-id dataset_<task_id> --frequency 1d --name "A 股日线"
./bin/moox-cli factor task list --space-id stockcn          # 记下任务 ID，确认状态；停用的话 factor task enable --task-id <ID>
```

需要用到复权价、财务比率的因子时，再建因子定义并加为成员（表达式写法、核对与确认流程见 [`factor-dsl.md`](factor-dsl.md)）：

```yaml
# mom_adj.yaml：复权后的 20 日涨幅。K 线是不复权价，close * adj_factor 是后复权价
params:
  window: {type: int, min: 2, max: 250, default: 20}
let:
  adj_close: close * adj_factor
output: adj_close / delay(adj_close, window) - 1
```

```yaml
# roe_ttm.yaml：滚动十二个月的净资产收益率
output: fin_net_profit_parent_ttm / fin_equity_parent
```

```bash
./bin/moox-cli factor create --space-id stockcn --factor-id mom_adj --name "复权20日涨幅" --spec-file mom_adj.yaml
./bin/moox-cli factor create --space-id stockcn --factor-id roe_ttm --name "净资产收益率TTM" --spec-file roe_ttm.yaml
./bin/moox-cli factor member add --space-id stockcn --task-id <日线因子任务> --factor-id mom_adj
./bin/moox-cli factor member add --space-id stockcn --task-id <日线因子任务> --factor-id roe_ttm
./bin/moox-cli factor member enable --space-id stockcn --task-id <日线因子任务> --member-key mom_adj
./bin/moox-cli factor member enable --space-id stockcn --task-id <日线因子任务> --member-key roe_ttm
```

- 成员加入时是停用的，`factor member enable` 之后才计算。启用不会自动补算历史，需要历史因子值时用 `factor recalc submit --space-id stockcn --task-id <任务> --from 2025-11-01T00:00:00Z --to 2026-11-11T00:00:00Z`（`--from`、`--to` 只接受 RFC3339，左闭右开，不能只写日期）。
- 前复权价本身（以当前这一根为基准）：k 根之前的前复权收盘价是 `delay(close, k) * delay(adj_factor, k) / adj_factor`。只算涨幅、偏离这类比值时用后复权价即可，结果相同。
- 财务字段为空（金融类公司没有的科目、缺上年数据的滚动值）时因子值也为空。
- 把这个因子任务和成员写进自己的因子任务文件，以后重装时用 `factor task apply --file` 导入。`mom_adj`、`roe_ttm` 已在内置因子库 `modules/factor/factors/stockcn_daily.yaml` 里（库里还有下面“内置的估值与筹码因子”列出的 9 个）。文件格式同仓库的 `config/setup/factor-tasks.yaml`，三项都要写：`source_dir`（因子库目录，相对这个文件所在的目录）、`tasks`、`definitions`。例如在仓库根目录建 `my-factors.yaml`：

  ```yaml
  # 因子库目录，相对这个文件所在的目录
  source_dir: modules/factor/factors
  tasks:
    - id: <日线因子任务>
      name: A股日线
      space_id: stockcn
      source_dataset_id: dataset_<task_id>
      freq: 1d
      members:
        - {factor: mom_adj}
        - {factor: roe_ttm}
  definitions:
    - {factor_id: mom_adj, file: stockcn_daily.yaml}
    - {factor_id: roe_ttm, file: stockcn_daily.yaml}
  ```

  ```bash
  ./bin/moox-cli factor task apply --file ./my-factors.yaml
  ```

  自己写的因子不在因子库里时，先把它加进 `source_dir` 下的库文件，否则导入报错，提示库文件里没有这个因子（`has no factor "<因子 ID>"`）。

### 内置的估值与筹码因子

内置因子库 `stockcn_daily.yaml` 里另有 9 个因子，源数据集要带对应的附加字段（内置任务 `dasftksvjhj2jom4vhj0` 都带）。用法与上面相同：在自己的因子任务文件（或仓库的种子 `config/setup/factor-tasks.yaml`）的 `definitions` 里加一行（`file: stockcn_daily.yaml`），任务的 `members` 里加成员，再用 `factor task apply --file` 导入（改的是种子时由 `setup start` 导入），或用 `factor create --spec-file` 手工建。

| 因子 ID | 含义 | 用到的附加字段 |
| --- | --- | --- |
| `total_mv`、`float_mv` | 总市值、流通市值（元）：当日不复权收盘价 × 股本 | `total_shares`、`float_shares` |
| `pe_ttm`、`pb`、`ps_ttm` | 市盈率、市净率、市销率：总市值 ÷ 归母净利润（滚动十二个月）、归母股东权益、营业收入（滚动十二个月） | `total_shares`、`fin_net_profit_parent_ttm`、`fin_equity_parent`、`fin_revenue_ttm` |
| `dv_ratio` | 股息率（%）：每股股利 ÷ 收盘价 | `dividend_per_share` |
| `chip_profit_ratio` | 筹码获利比例（0～1）：持仓成本不高于当日收盘价的占比 | `turnover_rate`、`adj_factor` |
| `chip_avg_cost` | 筹码平均成本，可以直接和当日不复权收盘价比 | 同上 |
| `chip_concentration` | 90% 筹码所在价格区间的相对宽度，越小越集中 | 同上 |

- 市值和估值因子的股本取最近 20 个交易日里最后一个有值的；开通以前没有股本，这些因子为空。亏损股的市盈率、净资产为负的市净率是负数，排序前按需要先过滤（例如 `filter: "daily.pe_ttm > 0"`）。分母为 0 或为空时因子值为空。
- 估值是用新浪财报自己算的，财报按披露日的下一个交易日生效，和行情软件上的数值会有小差异。
- 筹码因子的参数 `window` 默认 210、范围 20～500：用最近 `window` 个交易日的开高低收与换手率推算，上市不满 `window` 个交易日的股票没有值；没有复权因子的少数标的一直为空。某天没采到换手率时按当天没有换手算。平均成本在除权日会和收盘价同比例跳变，`daily.close / daily.chip_avg_cost` 不受影响。算法见 `docs/模块/因子.md` 第 6 节。
- 选小市值写 `score: "rank(-daily.float_mv)"`；偏离平均成本写 `daily.close / daily.chip_avg_cost - 1`（因子不能读另一个因子的结果，这类组合写在策略里）。
- 直接在策略里读 `daily.total_shares`、`daily.is_st` 这类源数据列时，某天没采到就是缺数（不会沿用前值）；需要沿用就在自己的日线因子里写 `ts_last_valid(total_shares, 20)`，或在规则上配 `on_missing`。

## 5. 在策略里用

主帧用日内周期（例如 5m），日线因子任务作为附加来源：

```yaml
name: ashare_industry_fundamental
bar: 5m
sources:
  daily: {bar: 1d}
rules:
  - id: pick
    type: rank
    filter: "daily.roe_ttm > 0"
    score: "rank(daily.roe_ttm) + rank(daily.mom_adj)"
    select: {top: 20, group: {by: daily.sw_l1, max: 3}}                 # 每个申万一级行业最多 3 只
    holding: {calendar: trading_days, every: 5, days: [1], at: first}
    weight: {total: 1}
portfolio: {max_missing: 0.2}
```

```bash
./bin/moox-cli strategy validate --space-id stockcn --dsl-file s.yaml \
  --factor-task equity=<5m 因子任务> --factor-task daily:equity=<日线因子任务>
./bin/moox-cli strategy create --dsl-file s.yaml
./bin/moox-cli strategy instance create --space-id stockcn --strategy-id <ID> \
  --factor-task equity=<5m 因子任务> --factor-task daily:equity=<日线因子任务>
```

- A 股的来源只能是 `1d`，主帧必须是日内周期。一个因子任务可以被多个实例引用；实例绑定的因子任务创建后不能改。
- 申万行业只有代码（`sw_l1` 如 `480000`），没有中文名。没有申万记录的股票分组列为空，按缺数处理，计入 `max_missing`。
- 日线实时采集失败时，要到再下一天 08:00 之后才自动补上，这期间来源列按缺数处理；不想因此清仓的规则配 `on_missing: {keep_bars: N}`，或者按第 3 节手工补采那一天。
- 完整的创建、启用、试运行流程见 [`strategy-dsl.md`](strategy-dsl.md)；例子的逐条说明见 `docs/策略DSL手册.md` 12.11。

## 6. 确认

```bash
./bin/moox-cli collector status --space-id stockcn
./bin/moox-cli collector kline check --space-id stockcn --dataset-id dataset_<task_id> --from 2025-11-01 --to 2026-11-11
./bin/moox-cli factor status --space-id stockcn
```

- `collector status`：日线数据集 `level: ok`，没有失败分块。
- `kline check`：`status: complete`、`missing: 0`。`--to` 不要晚于“已经采过的最后一个交易日的下一天”，今天的日线明天 08:00 才有。停牌的交易日来源没有数据，补采确认后记在 `absent_at_source`，不算缺口。
- 用 `moox-cli data kline get`（[`data-query.md`](data-query.md)）查询 A 股日线时：若同时启用了纯日线与带附加字段两条 1d 任务，直接传 `--dataset-id` 区分（要看附加字段则显式加上 `--columns`）。
- `kline check --deep` 不检查附加字段（它们允许为空）。要看附加字段有没有值，在管理台数据页打开带附加字段那条任务的结果数据集取几行看（或用 `moox-cli data kline get --space-id stockcn --dataset-id <结果数据集ID> --interval 1d --symbol <标的> --columns open,close,adj_factor,sw_l1,turnover_rate`）：抽一只近期除过权的股票看 `adj_factor` 在除权日变化；抽一只银行股，`fin_total_revenue` 为空是正常的。
- 管理台“数据采集 › 数据源”页下方的“附加字段来源”应列出复权因子、申万行业、财务报表、换手率、股本与股利五个来源和它们的字段。
- 换手率、股本：在管理台数据页取最近一个交易日的几行，`turnover_rate` 与行情软件的换手率一致，`total_shares × close` 与总市值一致。股本只在“接口标注的交易日”那一行有值，补出来的历史行为空是正常的。

## 常见问题

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `collector task update` 报“输出字段只能减少，不能新增” | 想给已有任务加字段 | 新建采集任务（2b）和因子任务；原来的任务按需停用或删除 |
| 管理台新建任务时看不到复权、行业、财务、换手率、股本字段 | 标签不是 A 股股票、频率不是 `1d` | 检查标签和频率（第 1 步看 `extra_fields` 的适用范围） |
| 某天的日线整只股票没有行 | 五个附加来源里有一个当时报错，带附加字段的任务整行不写（纯日线任务不受影响）。其中“选股器的数据还停在某天”表示取数时东方财富还没更新到这个交易日 | 等自动补采（隔一天），或手工 `kline backfill` 那一天 |
| 某只股票 `adj_factor` 一直为空 | 新浪复权接口对少数标的返回空列表 | 目前没有别的来源；因子里按缺数处理 |
| 启用或停用日线任务后几分钟了 `collector fetch-plan` 还是未生效 | 计划在等下发确认（云函数操作慢、失败），或数据集在等旧份过窗口 | 看 `collector status` 的等待原因与失败原因 |
| 申万行业全部失败，日志里有证书校验错误 | 申万网站换了证书链，内嵌的中间证书不再适用 | 需要更新程序里内嵌的证书，向用户说明后停下，不要关闭校验 |
| 某只股票 `sw_l1` 一直为空 | 申万文件里没有它的记录（新股等） | 正常；策略里按缺数处理 |
| 补出来的历史里 `total_shares` 等股本列全是空的 | 股本来源只有当日值，没有历史 | 正常；市值、估值因子从开通日起才有值 |
| 筹码因子最近一段才有值 | 默认回看 210 个交易日，源数据的历史不够 | 把日线和换手率的历史补够（第 3 节），再 `factor recalc submit` |
| 15:00 那一根策略结果到第二天早上才出 | 日线次日 08:00 才采集 | 设计如此 |
