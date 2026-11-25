# 初始数据导入

数据源（空间 + 数据集 + 频率）第一次接入时，把用户提供的历史 K 线一次灌进系统，让因子从第一期起就有完整的长回看（例如 EMA(50000)），
而不是上线后再等实时数据慢慢积累。

**两层写入：** 因子机本地存储写**全量**历史（不自动淘汰；只有 `setup wipe` 或停掉引擎后删 `<部署根>/data/factor-engine` 才会清空）；Storage Primary 只写每个文件**最后 6000 根**中仍在 Primary 保留期内的部分，
之后由实时采集与补采接上。View 写入时还会按自己的 `[storage_view.max_age]` 丢弃超期行，因此可能只包含导入尾部的一部分，甚至为空；默认 crypto 1m 的 View 上限为 7d，Primary 为 35d。导入**不触发**历史因子与策略结果的计算，也**不推送告警**（导入期间屏蔽 Monitor 告警推送）。

Agent 按下面的步骤逐步执行，每一步的输出都要核对再进入下一步；任何一步出错先停下来向用户汇报，不要跳步。

## 0. 前置

- 数据集与采集任务已建好（K 线数据集由采集任务创建，见 `moox-cli collector task create`），标签成员已就绪，实时采集（WS 或拉取）已在写入。
- 数据集第一次接入时 Collector 会自动补历史（`moox-cli collector status` 的“历史补齐”，补到保留期与行情源允许的深度，补齐期间因子不计算、策略不下单）。
  导入包要覆盖的是保留期以外的长历史，和自动补齐不冲突，但两者写同一段数据没有意义：要导入就先 `moox-cli collector history-seed skip --space-id <空间>`（取消剩余分块，已补进去的保留），或等补齐结束再导入。
- 该数据源**还没有启用的因子任务与策略实例**（初始导入期间本数据集的 View 会积压，追平之后再启用）。
- 工具：`moox-cli` 必须是仓库当前版本（`go build -o <临时目录>/moox-cli ./cmd/moox-cli`，在 `modules/cli`），与线上部署的版本一致。
- 代理是否保留以当前网络环境为准；下面的示例对 MooX 内网 / SSH 调用移除 HTTP(S) 代理，不代表其他工具只有 git 才能使用代理。

## 1. 导入包（用户按这个格式整理）

```text
import-pack/
  manifest.json
  <freq>/<SUBJECT>.csv          例如 5m/BTC-USDT.csv；文件名（去掉 .csv）就是标的 ID
```

- **CSV**：UTF-8、无 BOM、LF；首行表头 `data_time` 加数据集的**全部字段**（如 `open,high,low,close,volume,quote_volume,trade_num`）；
  `data_time` 是 K 线开盘时间，UTC RFC3339（`2026-01-01T00:00:00Z`），严格升序、无重复、对齐频率网格。序列内部缺周期允许（因子机本地按字段的 `fill` 规则写成带标记的填充行，Storage 不写填充行）。
- **manifest.json**：

  ```json
  {
    "space_id": "crypto",
    "dataset_id": "dataset_xxx",
    "freq": "5m",
    "series_tag": "venue:binance|market:swap|source:swap_http",
    "files": [
      {"subject_id": "BTC-USDT", "file": "5m/BTC-USDT.csv", "rows": 60000,
       "first_utc": "2026-03-25T16:00:00Z", "last_utc": "2026-10-19T23:55:00Z", "sha256": "…"}
    ]
  }
  ```

- **硬约束**（预检强制，有问题一次列全、不写任何内容）：
  1. 每个文件最后一根（`last_utc`）早于**现在 − 2 天**（历史段只含已定稿的数据，不和实时写入重叠）；
  2. 最后一根到现在的缺口不超过 `min(30 天, 该频率 Storage 保留期 − 1 天)`——这段要靠 REST 补采，且必须整段留在 Storage 里，因子机同步器才能接着拉取（否则这段会被写成填充行）；
  3. 每个文件行数不少于该数据源的回看数量（7×24 市场是引擎的 `window.lookback`，有交易时段的市场是 `window.session_lookback`，有 `lookback_overrides` 时取覆盖值；用 `--min-rows` 传入）；
  4. 标的必须已在数据集的标签里；字段与类型和数据集严格一致；`sha256`、行数、首尾时间与文件一致。
- `series_tag` 必须与实时采集写入的**完全一致**，否则历史和增量会变成两条序列。第 3 步核对。
- 数据集选了 `funding_rate`（币安永续 1h/1d）时，CSV 也要带这一列：值是结算时刻落在 `[data_time, data_time + 一根)` 内的资金费率之和（小数原值，本根没有结算写 0；结算可能晚 1 毫秒，按原始毫秒归属）。
  公开数据站的资金费只有月度文件，按每个合约的结算间隔检查结算是否连续，缺月时报错，不能写成 0。这一列的 `fill` 是 `zero`，缺周期的填充行写 0。

## 2. 预检（操作机，只读）

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli import --pack <包目录> --target storage --dry-run --min-rows <回看数量> \
  --tail-rows 6000 --allow-missing --file ./moox.toml > preflight.json
```

`--min-rows` 是必填的硬约束；`--allow-missing` 让允许缺周期的包通过；`--tail-rows` 必须与第 7 步一致，预检输出里才有每个文件的**尾部窗口起点与行数**（`tail_from`、`tail_rows`），用来确认窗口符合预期。
有任何一项不合格就停下，把问题原文交给用户整理包；不要自己改包。回看数量查因子机 `/data/moox/config/factor-engine/engine.yaml` 的 `window.lookback`（7×24 市场）或 `window.session_lookback`（有交易时段的市场，未写时默认 5000），数据源有 `lookback_overrides` 的取覆盖值。

## 3. 核对 series_tag（操作机，只读）

取目标数据集最近的一条实时行，对比其 `series_tag` 与清单：

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli data rows export --file ./moox.toml --space-id <空间> --dataset-id <数据集> --frequency <频率> \
  --subject <任一标的> --columns close --from <最近几分钟> --to <现在> --page-size 2
```

`key.series_tag` 与 `manifest.json` 的 `series_tag` 不一致就停下汇报。

## 4. 屏蔽告警推送

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli monitor mute --for 3h --reason 初始数据导入 --file ./moox.toml
```

屏蔽期间 Monitor 的探测、入库、告警评估与记录照常，只是不推送；到期自动恢复，结束时补发一条汇总。导入预计超过 3 小时就把 `--for` 调大（最长 24h）。
`moox-cli monitor mute-status` 查看状态。

## 5. 补缺口（操作机）

文件最后一根到现在的缺口用 REST 补采。范围从**文件末根的下一期**到**实时写入的起点**（新数据集的实时写入起点就是 `moox-cli collector status --file ./moox.toml --space-id <空间>` 里该数据集的 `effective_from`，即首份采集计划的生效周期；也可以用第一批 WS 数据的时间核对。已经在写入时取最近已收盘的一根）：

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli collector kline backfill --file ./moox.toml --space-id <空间> --dataset-id <数据集> --force --from <末根+1期> --to <实时起点>
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli collector kline backfill-status --file ./moox.toml --space-id <空间> --job-id <作业ID>   # 等到没有 pending/running
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli collector kline backfill-retry  --file ./moox.toml --space-id <空间> --job-id <作业ID>   # 有失败分块时重试，直到全部成功
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli collector kline check --deep --file ./moox.toml --space-id <空间> --dataset-id <数据集> --from <末根+1期> --to <现在>
```

`check` 必须是 `missing: 0`，但**新数据集的采集计划生效点是创建当天，补采段在生效点之前时 `expected` 为 0，这时 `missing: 0` 不能说明任何事**，以行数为准：
用几个标的 `data rows export`（`--page-size` 大于应有根数）数一下 `[末根+1期, 实时起点)` 的行数是否等于应有根数（按 5m 每天 288 根算）。

## 6. 因子机本地全量导入（操作机，经网关写因子机）

`--target factor` 经操作员网关调因子引擎 `trpc.moox.factor.EngineControl` 的 `WriteSeries`（因子机只能出站时经 control 中转），
文件留在操作机上，不传到因子机，也不在因子机上运行 moox-cli：导入由操作机上的 moox-cli 用操作员网关签名发出。
（因子机上的进程只能以因子机上的组件身份签名，调不了只给 moox-cli 的方法，见[主机网关](../../../docs/模块/主机网关.md)。）

数据从操作机经 control 绕到因子机，**中转没有限速**：control 的出口与采集写 Storage 走的是同一条跨地域公网，一路 TCP 就可能把它占满，挤占生产采集
（先例：从海外机 rsync 一个 81 MB 的文件让 1m 因子晚了 8 分钟）。`xargs -P` 限制的是进程数，不是带宽。所以：

- **先估体量**：行数 ×（数值列数 × 约 9 字节 + 时间与 tag 约 30 字节），批次经 zstd 压缩后再发，压缩能省一部分，但长历史的总量仍可能到 GB 级；
  例如 500 个标的、每个 5 年 5m 线（约 52 万行）、20 列，约 500 × 52 万 × 210 字节 ≈ 55 GB 原始量。先用一个标的试跑并记下耗时，按耗时估算全量要多久、告诉用户。
- **从 1 路开始**：导入时盯着采集延迟（告警已屏蔽，看首页的数据新鲜度与监控页数据链路的到达延迟），没有变慢再考虑加到 2 路；变慢就停下来，挑采集空闲的时段再导。

一批（`--batch-rows`，默认一万行）编码后不能超过 4 MiB，超了按提示减小 `--batch-rows`。

```bash
# 试跑一个标的
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli import --file ./moox.toml --target factor --space-id <空间> --dataset-id <数据集> --frequency <频率> \
  --series-tag '<series_tag>' --allow-missing --job-db ./one.db --path <包目录>/<频率>/<标的>.csv
# 全量：每个文件一个进程、各自独立的断点库；经 control 中转没有限速，从 1 路开始，盯着采集延迟再决定加不加
ls <包目录>/<频率>/*.csv | xargs -P 1 -I{} sh -c 'env -u HTTP_PROXY -u HTTPS_PROXY moox-cli import --file ./moox.toml --target factor --space-id <空间> --dataset-id <数据集> --frequency <频率> \
  --series-tag "<series_tag>" --allow-missing --job-db "./jobs/$(basename {}).db" --path {} > "./logs/$(basename {}).log" 2>&1 \
  && echo ok >> ok.txt || echo "{}" >> fail.txt'
```

（`./jobs`、`./logs` 先 `mkdir`。）全部结束后 `wc -l ok.txt fail.txt`；`fail.txt` 里的文件看日志原因后重跑（导入是幂等的，整段替换）。

## 7. Storage 窗口（操作机，可与第 6 步同时进行）

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli import --pack <包目录> --target storage --tail-rows 6000 --min-rows <回看数量> --allow-missing --wait-view=false \
  --file ./moox.toml --job-db ./storage-import.db > storage-import.json
```

输出（每个文件的 `tail_from`、`tail_rows`）存成 `storage-import.json`，第 9 步的逐标的校验要用它。

只写每个文件最后 6000 行中仍在 Primary 保留期内的部分。输出的 `tail_from` / `tail_rows` 是 Primary 导入窗口，未包含 View 写入时按当前时钟进行的年龄过滤。Storage 写入在 storage-primary 的 Pebble 库内串行，单进程即可；要并发也不超过 2～4 路（再多只是排队）。
`--job-db` 保留着，中断后原样重跑自动续传。失败的批（Storage 写超时）重跑即可。
`--wait-view=false`：`import` 默认写完后最多等 30 秒让该数据集的 View 跟上，长历史导入 View 要追更久，这里不等，按第 8 步单独确认追平。

### 7a. 长历史进归档（可选）

保留期内的数据写进 Storage 后会被归档每分钟跟随；**需要保留期之外的长历史进归档时**，在第 6 步或单独一次导入里加 `--target archive`（可与 `storage,factor` 组合，也可只写归档）。前提：数据集已有归档任务、归档 worker 在线；`series_tag` 必须非空（导入包清单或 `--series-tag` 给出）。
导入要先在 worker 暂存库里装下整次导入：按约 `(40 + 10 × 值列数) × 1.5` 字节/行估算，再加上时间范围内已登记分区的行数（落在已定稿分区的批次会把整个分区读回）。预检超过 worker 可用空间减 `min_free_bytes` 就拒绝，这时按时间拆成几次导入。
因子机只出站时行经 control 中转写进 worker，较慢；导入期间该任务的分钟跟随暂停，结束后接着读。

## 8. 等本数据集的 View 追平

- `moox-cli doctor alerts --file ./moox.toml` 里该 View 不再报“等待中”（屏蔽期间告警仍照常评估与记录，只是不推送）；
- 对比 View 与 Primary 的输出水位指标，或通过 DataView 按数据集查询（`data view state --dataset-id <数据集>`）；`data kline get --limit 1` 只读 Primary，不能证明 View 已追平。
- 历史行已超过 View 年龄上限时，缺行属于预期；查看 `moox_storage_view_expired_rows_dropped_total{space,freq}`，用未超期的补采或实时行验证 View 追平，不要求 View 与整个 Primary 导入尾窗行数相等。

追不平就停下汇报，不要靠重启或重建 View 解决（`repair-view` 是破坏性运维，需要用户确认，见 `cli-operations.md`）。

## 9. 校验（只比边界与行数）

- **Storage Primary**：`collector kline check --from <末根+1期> --to <现在>` 为 `missing: 0`；**每个标的**在 `[tail_from, 文件末根]` 内的行数等于 CSV 在同一区间内的行数（不以 View 做这项全窗口核对）——
  用脚本逐个核对（`tail_from` 取自第 7 步**实际导入**的输出 `storage-import.json`，已经包含了保留期截止点；不要用第 2 步的预检输出——截止点每次运行都重新计算，两步相隔久了会后移）：

  ```bash
  python3 -I skills/moox/scripts/import_verify.py --pack <包目录> --preflight storage-import.json --moox-cli <moox-cli 路径> --moox-toml ./moox.toml
  ```

  退出码非零或有“行数不一致 / 读取失败”的标的就停下汇报（读取失败多半是 Storage 忙，稍后重跑脚本即可）。
- **因子机本地**：每个标的本地最早一根等于 `manifest.json` 的 `first_utc`；因子机同步器接上之后本地最晚一根等于 Storage 最晚一根；
  `[文件末根, 现在]` 内没有填充行（有说明缺口没补全）。本地边界用引擎接口：`moox-cli gateway call trpc.moox.factor.EngineControl GetSeriesBounds --file ./moox.toml --data '{"space_id":"…","dataset_id":"…","freq":"…","subject_id":"…"}'`。

## 10. 解除屏蔽

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli monitor unmute --file ./moox.toml
```

到期也会自动解除；解除时若仍有未恢复的告警或期间触发后又恢复的，Monitor 补发一条汇总。

## 11. 启用

建因子任务或策略实例。**启用不会自动补算历史**：因子从引擎同步到新目录之后的周期开始计算（目录每分钟第 45 秒同步一次，
实际要等本地窗口就绪、任务检查通过，且该期输入与成员预热要求满足后才出结果）；启用到同步之间收盘的周期不会补算，预热期间跳过的周期由引擎的跳过账本尝试恢复，其余的需要时手动补算。

- **新建的因子任务不需要任何生命周期命令**：引擎在计算前检查每个任务（窗口载入、本地序列覆盖最长回看、变更同步等），检查通过就计算，
  不通过就不计算并写明哪一项没过；每 30 秒重查一次，窗口载入完成后立即重查。
  时间线：任务建好后，引擎在每分钟第 45 秒同步目录，预加载本地窗口（源数据有 5 万根历史时可达一两分钟），检查通过后再等下一个源数据周期收盘出第一批结果。
  在操作机上用 `moox-cli factor task status --task-id <任务>` 查看逐项检查与处理办法；长时间不计算时按给出的原因处理（例如导入历史）。
- 同一个因子要用不同的固定参数算多组时，不要为参数新建因子定义：用同一个定义加多个成员，每个成员带自己的固定参数，成员键按参数生成
  （全部参数等于默认值时就是因子 ID，否则“因子ID__值1_值2…”）。例如 EMA20 用 5 万根预热：`ema` 成员 `span=20, warmup=50000`，成员键与结果列都是 `ema__20_50000`；
  固定参数加成员之后不再改。在 `moox.toml` 里写成员的 `{ factor = "ema", params = { span = 20, warmup = 50000 } }`，页面里是添加因子时填参数（页面会预览成员键和列名）。
  需要由策略逐期调参的因子加成“可变成员”（`{ factor = "ma_fast", var = true }`，列名 `ma_fast__var`）。

- 策略 `min_age_bars` 按关联因子任务的结果 View 的覆盖判断：任务可以先于策略运行积累历史，结果 View 的覆盖不够时启用被拒绝。
- 用到 `bars[-1]` 因子列的规则，启用后的第一期会以 `previous_version_unknown` 整期跳过，第二期起正常（新实例没有持仓，不会触发退出）。
- 需要历史因子值时再手动补算：`moox-cli factor recalc submit --task-id <任务> --from … --to …` 指定范围。每个补算块会往 Storage 写几十万行因子结果，
  Storage 的 View 约 1 万行/秒才消化得完，范围按需、一次不要太大。

## 常见问题

- **导入后 View 长时间追不平**：先看 `storage-view` 慢日志（storage 主机 `/data/moox/logs/storage-view/trpc.log`，关键词“较慢”；这个文件只留最近 50MB×5，更早的在 CLS 里查）与 CPU profile
  （storage-view 的 admin 端口 `localhost:20001/debug/pprof/profile`，只监听本机）。积压的主要原因是写进 View 的行数太多——所以 Storage 只写尾部窗口。
- **Storage 写入超时**：Storage 主机只有 4 核时，同时有补算或大批量导入会超时；降低并发或错开。重跑是幂等的。
- **保留期**：Storage 只需装下“尾部窗口 + 缺口 + 实时”，默认保留期够用，不必为导入临时调大。
