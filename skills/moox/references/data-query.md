# 查询采集的 K 线

用户要取采集到的 K 线数据时，用 `moox-cli data skill kline get`。这是只读的 RPC 查询，不使用浏览器登录会话。

## 确定命令与配置

从当前已加载的 `SKILL.md` 得到它所在目录的真实绝对路径，作为 `SKILL_ROOT`。下面每个示例里的 `/absolute/path/resolved-from-the-loaded-SKILL.md` 都是占位值：Agent 执行代码块前必须把它换成那个真实目录，绝不能原样使用占位值。调用 `"$SKILL_ROOT/scripts/data-kline.sh"` 这个封装脚本；不要根据 shell 的工作目录推断 Skill 的位置，也不要直接调用 `moox-cli`。

封装脚本通过 `BASH_SOURCE` 定位自己的 Skill 根目录，注入包内 `config/data-access.yaml` 的绝对路径，拒绝调用方传入的 `--config` 或 `--file`，查找 `moox-cli` 时先看仓库的 `../../bin/moox-cli`，再看 `PATH`。配置文件不存在、是符号链接或权限不是 `0600` 时，脚本拒绝执行。

包内的 `gateway_client` 指定一个外部入口（`entry_address` 是入口主机地址加 `11004`，`entry_host` 是入口主机 ID）和 `moox-skill` 调用方。`key_file` 是 `setup export-skill-config` 在本机生成的 `moox-skill` ed25519 私钥（Admin 只保存它的公钥）；它相对 `config/data-access.yaml` 解析，指向随包的签名密钥，密钥权限 `0600`，放在权限 `0700` 的 `.moox-skill-keys` 目录里。CLI 向入口主机网关的外部入口建立签名的原生 tRPC 连接，不需要 SSH 身份、目录缓存或 CA 证书包。同一份配置里还带着数据集目录。用 `setup export-skill-config` 生成它，或者用 `scripts/build/package-skill.sh` 打包 Skill。

另一条操作员命令 `moox-cli data kline get` 用的是操作员的 SSH 配置。Skill 查询一律用随包的封装脚本和 `moox-skill` 外部身份，从入口主机网关的外部入口（`11004`）进入。

Skill 压缩包有意不带跨平台的 `moox-cli` 二进制。它依赖仓库里的那个二进制，或者 `PATH` 上已安装的 `moox-cli`；两者都没有时封装脚本会明确报错。

每次查询都在一次 shell 调用里写完整：

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
"$SKILL_ROOT/scripts/data-kline.sh" \
  --data-type crypto \
  --symbol BTC-USDT \
  --interval 1m
```

不要传 `--config`，不要依赖 CLI 的相对默认配置路径，也不要用 `MOOX_SKILL_CONFIG`。不要读取、打印、`source` 或复制包内的配置；它和签名私钥放在一起。

## 把请求对应到参数

- 必须给 `--data-type`。加密货币或 crypto 对应 `crypto`，A 股对应 `stockcn`。
- 用户指明了交易所就加 `--exchange`。否则省略，使用该数据类型配置的默认值；`crypto` 目前默认 `binance`。
- 用户给的 `--symbol` 原样传入。不要把 `BTC-USDT` 改写成 `BTCUSDT`，不要改大小写，也不要猜交易对。
- 按用户要求传 `--interval`，并严格按包内目录来。当前的初始目录导出了币安现货 `1m`，以及 A 股（`stockcn`，沪深 300）的 `5m` 和 `1d`；旧的包里条目可能更少。采集任务变更后要重新生成包。
- 用户要指定条数时用 `--limit`（`1..1000`）；否则 CLI 默认 `100`。
- 只有用户给了时间范围才用 RFC3339 的 `--from` 和 `--to`。起始时间必须严格早于结束时间。
- 需要额外字段时用 `--columns`（逗号分隔）；默认是 `open,high,low,close,volume`，所以 `quote_volume` 和 `trade_num` 要显式选上。用 `--timeout` 覆盖默认 15 秒的 RPC 超时；只有用户要结果文件时才用 `--output`。

示例遵循同样的替换规则。每个代码块把自己的 `SKILL_ROOT` 换好后都可以独立执行。

“获取加密货币 BTC-USDT 的 1m K 线”:
```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
"$SKILL_ROOT/scripts/data-kline.sh" \
  --data-type crypto --symbol BTC-USDT --interval 1m
```

“获取币安 BTC-USDT 最新 20 根 1m K 线”:
```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
"$SKILL_ROOT/scripts/data-kline.sh" \
  --data-type crypto --exchange binance \
  --symbol BTC-USDT --interval 1m --limit 20
```

“获取币安 BTC-USDT 在指定 UTC 时间范围内的 1m K 线”:
```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
"$SKILL_ROOT/scripts/data-kline.sh" \
  --data-type crypto --exchange binance \
  --symbol BTC-USDT --interval 1m \
  --from 2026-08-28T00:00:00Z --to 2026-08-28T01:00:00Z
```

CLI 按 `data-type + exchange + interval` 在包内目录里精确查找。不要自己拼或猜 Space、Dataset、频率或 series tag。在初始目录里，`crypto/binance/1m` 对应币安现货 1m 采集任务的结果 `crypto/dataset_dasftksvjhj2jom4vhd0`，频率 `1m`，series tag 为 `venue:binance|market:spot|source:spot_http`。目录不支持的组合按错误汇报，不要退回到别的数据集。

## 处理结果

成功时，解析 protobuf JSON 响应，汇总实际解析到的数据类型、交易所、标的、周期、行数和返回的时间范围。保持返回的顺序：不带时间范围的最新数据查询是降序；带了任一时间边界的查询是升序，返回匹配的最早 `limit` 行。不给起始时间默认是 Unix 纪元，不给结束时间默认是当前时间。范围是 `[start, end)`。行集为空表示查询成功但没有采集到数据，不是 RPC 失败。

输出和诊断信息里不要包含 Gateway 密钥、Storage app key、请求签名、配置内容或完整的带鉴权请求。
