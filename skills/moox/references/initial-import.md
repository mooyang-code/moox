# 初始数据导入

数据源（空间 + 数据集 + 频率）第一次接入时，把用户提供的历史 K 线一次灌进系统，让因子从第一期起就有完整的长回看（例如 EMA(50000)），
而不是上线后再等实时数据慢慢积累。设计与取舍见 [`docs/计划/初始数据导入执行计划.md`](../../../docs/计划/初始数据导入执行计划.md)。

**两层写入：** 因子机本地存储写**全量**历史并永久保留；Storage（与 View）只写每个文件**最后 6000 根**中仍在保留期内的部分，
之后由实时采集与补采接上。导入**不触发**历史因子与策略结果的计算，也**不推送告警**（导入期间屏蔽 Monitor 告警推送）。

Agent 按下面的步骤逐步执行，每一步的输出都要核对再进入下一步；任何一步出错先停下来向用户汇报，不要跳步。

## 0. 前置

- 数据集与采集任务已建好（K 线数据集由采集任务创建，见 `moox-cli collector task create`），标签成员已就绪，实时采集（WS 或拉取）已在写入。
- 该数据源**还没有启用的因子集与策略实例**（初始导入期间本数据集的 View 会积压，追平之后再启用）。
- 工具：`moox-cli` 必须是仓库当前版本（`go build -o <临时目录>/moox-cli ./cmd/moox-cli`，在 `modules/cli`）；`bin/` 里的旧版读不了新的 `moox.toml`。
- 用 `env -u HTTP_PROXY -u HTTPS_PROXY` 运行 `moox-cli`（git 才需要代理）。

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
  3. 每个文件行数不少于该数据源的回看数量（引擎 `window.lookback`，用 `--min-rows` 传入）；
  4. 标的必须已在数据集的标签里；字段与类型和数据集严格一致；`sha256`、行数、首尾时间与文件一致。
- `series_tag` 必须与实时采集写入的**完全一致**，否则历史和增量会变成两条序列。第 3 步核对。

## 2. 预检（操作机，只读）

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli import --manifest-pack <包目录> --target storage --dry-run --min-rows <回看数量> --manifest ./moox.toml
```

输出每个文件的行数、首尾时间、**尾部窗口起点与行数**（`tail_from`、`tail_rows`，不加 `--tail-rows` 时没有）。有任何一项不合格就停下，
把问题原文交给用户整理包；不要自己改包。回看数量查因子机 `/data/moox/factor-engine/config/engine.yaml` 的 `window.lookback`（数据源有 `lookback_overrides` 的取覆盖值）。

## 3. 核对 series_tag（操作机，只读）

取目标数据集最近的一条实时行，对比其 `series_tag` 与清单：

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli data rows export --file ./moox.toml --space <空间> --dataset <数据集> --freq <频率> \
  --subject <任一标的> --columns close --start-time <最近几分钟> --end-time <现在> --page-size 2
```

`key.series_tag` 与 `manifest.json` 的 `series_tag` 不一致就停下汇报。

## 4. 屏蔽告警推送

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli monitor mute --for 3h --reason 初始数据导入 --file ./moox.toml
```

屏蔽期间 Monitor 的探测、入库、告警评估与记录照常，只是不推送；到期自动恢复，结束时补发一条汇总。导入预计超过 3 小时就把 `--for` 调大（最长 24h）。
`moox-cli monitor mute-status` 查看状态。

## 5. 补缺口（操作机）

文件最后一根到现在的缺口用 REST 补采。范围从**文件末根的下一期**到**实时写入的起点**（`ingest-mode get` 与第一批 WS 数据的时间可确认；已经在写入时取最近已收盘的一根）：

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli collector kline backfill --file ./moox.toml --space-id <空间> --dataset-id <数据集> --force --from <末根+1期> --to <实时起点>
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli collector kline backfill-status --file ./moox.toml --space-id <空间> --job-id <作业ID>   # 等到没有 pending/running
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli collector kline backfill-retry  --file ./moox.toml --space-id <空间> --job-id <作业ID>   # 有失败分块时重试，直到全部成功
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli collector kline check --deep --file ./moox.toml --space-id <空间> --dataset-id <数据集> --from <末根+1期> --to <现在>
```

`check` 必须是 `missing: 0`。用一两个标的 `data rows export` 数一下 `[末根+1期, 现在)` 的行数是否等于应有根数。

## 6. 传文件到因子机（rsync）

```bash
# 因子机地址与 SSH 用户见 moox.toml 的 [hosts.factor]（不要读不要改 moox.toml，向用户确认）
env -u HTTP_PROXY -u HTTPS_PROXY TARGET_GOOS=linux TARGET_GOARCH=amd64 scripts/build/build.sh cli   # 产出 linux 版 moox-cli，目录见脚本输出
rsync -a --partial <包目录>/ <用户>@<因子机>:<导入目录>/pack/
rsync -a <linux 版 moox-cli> <用户>@<因子机>:<导入目录>/moox-cli
```

SSH 只用公钥。因子机上的目录放在数据盘（例如 `/data/moox/import`），不要放在系统盘小分区。

## 7. 因子机本地全量导入（在因子机上执行，不需要 moox.toml）

引擎的本地写入接口鉴权密钥来自本机的 `secrets/health-auth.env`，只放进环境变量，不打印、不写入脚本或日志：

```bash
ssh <用户>@<因子机>
cd <导入目录>
set -a; . /data/moox/factor-engine/secrets/health-auth.env; set +a
```

**并发按本机配置算**：`P = clamp(核数 ÷ 2, 2, 32)`，再按“可用内存 ÷ 0.5 GiB”取下限（给引擎的实时计算留一半核）。先用一个标的试跑并记下耗时，再全量：

```bash
nproc; free -g
# 试跑一个标的
./moox-cli import --target factor --space <空间> --dataset <数据集> --freq <频率> --series-tag '<series_tag>' --allow-missing \
  --engine http://127.0.0.1:11417 --job-db ./one.db --path pack/<频率>/<标的>.csv
# 全量：每个文件一个进程、各自独立的断点库，并发 P
ls pack/<频率>/*.csv | xargs -P <P> -I{} sh -c './moox-cli import --target factor --space <空间> --dataset <数据集> --freq <频率> \
  --series-tag "<series_tag>" --allow-missing --engine http://127.0.0.1:11417 --job-db "./jobs/$(basename {}).db" --path {} > "./logs/$(basename {}).log" 2>&1 \
  && echo ok >> ok.txt || echo "{}" >> fail.txt'
```

（`./jobs`、`./logs` 先 `mkdir`。）全部结束后 `wc -l ok.txt fail.txt`；`fail.txt` 里的文件看日志原因后重跑（导入是幂等的，整段替换）。

## 8. Storage 窗口（操作机，可与第 7 步同时进行）

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli import --manifest-pack <包目录> --target storage --tail-rows 6000 --min-rows <回看数量> --allow-missing \
  --manifest ./moox.toml --job-db ./storage-import.db
```

只写每个文件最后 6000 行中仍在保留期内的部分。Storage 写入在 DataNode 内串行，单进程即可；要并发也不超过 2～4 路（再多只是排队）。
`--job-db` 保留着，中断后原样重跑自动续传。失败的批（Storage 写超时）重跑即可。

## 9. 等本数据集的 View 追平

- `moox-cli doctor alerts --file ./moox.toml` 里该 View 不再报“等待中”（屏蔽期间告警仍照常评估与记录，只是不推送）；
- 或 View 最新一根等于数据集最新一根（`data kline get` 的 `--limit 1`）。

追不平就停下汇报，不要靠重启或重建 View 解决（`repair-view` 是破坏性运维，需要用户确认，见 `cli-operations.md`）。

## 10. 校验（只比边界与行数）

- **Storage**：`collector kline check --from <末根+1期> --to <现在>` 为 `missing: 0`；每个标的在 `[max(尾部首行, 现在 − 保留期 + 1 天), 文件末根]` 内的行数等于 CSV 在同一区间内的行数
  （预检输出的 `tail_from` 就是这个起点，沿用它）。抽样 8 个标的，必须含导入时失败重试过的。
- **因子机本地**：每个标的本地最早一根等于 `manifest.json` 的 `first_utc`；因子机同步器接上之后本地最晚一根等于 Storage 最晚一根；
  `[文件末根, 现在]` 内没有填充行（有说明缺口没补全）。本地边界用引擎接口：`GET http://127.0.0.1:11417/v1/series/bounds?space=…&dataset=…&freq=…&subject=…`（带健康鉴权，因子机上执行）。

## 11. 解除屏蔽

```bash
env -u HTTP_PROXY -u HTTPS_PROXY moox-cli monitor unmute --file ./moox.toml
```

到期也会自动解除；解除时若仍有未恢复的告警或期间触发后又恢复的，Monitor 补发一条汇总。

## 12. 启用

建因子集或策略实例。**启用不会自动补算历史**：因子从启用那一期开始计算（本地窗口完整，第一期就能算出所有标的）。

- 策略 `min_age_bars` 按绑定的 **K 线 View** 判断，不看私有因子结果 View。
- 用到 `bars[-1]` 因子列的规则，启用后的第一期会以 `previous_version_unknown` 整期跳过，第二期起正常（新实例没有持仓，不会触发退出）。
- 需要历史因子值时再手动补算：`moox-factor-mgr-cli recalc` 指定范围。每个补算块会往 Storage 写几十万行因子结果，
  Storage 的 View 约 1 万行/秒才消化得完，范围按需、一次不要太大。

## 常见问题

- **导入后 View 长时间追不平**：先看 `storage-view` 慢日志（storage 主机 `/data/moox/run/logs/storage-view.log`，关键词“较慢”）与 CPU profile
  （storage-view 的 admin 端口 `localhost:20001/debug/pprof/profile`，只监听本机）。积压的主要原因是写进 View 的行数太多——所以 Storage 只写尾部窗口。
- **Storage 写入超时**：Storage 主机只有 4 核时，同时有补算或大批量导入会超时；降低并发或错开。重跑是幂等的。
- **保留期**：Storage 只需装下“尾部窗口 + 缺口 + 实时”，默认保留期够用，不必为导入临时调大。
