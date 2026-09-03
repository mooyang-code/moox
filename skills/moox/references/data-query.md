# Collected K-line queries

Use `moox-cli data kline get` when the user asks for collected candlestick data. This is a read-only RPC query and does not use a browser login session.

## Resolve the command and config

From the currently loaded `SKILL.md`, obtain its real absolute parent directory and use that as `SKILL_ROOT`. In every example below, the value `/absolute/path/resolved-from-the-loaded-SKILL.md` is a metavalue: the Agent must replace it with that real parent directory before executing the block and must never use the metavalue literally. Invoke the wrapper at `"$SKILL_ROOT/scripts/data-kline.sh"`; do not infer the Skill location from the shell working directory and do not call `moox-cli` directly.

The wrapper locates its Skill root from `BASH_SOURCE`, injects the absolute packaged `config/data-access.yaml`, rejects caller-supplied `--config`, and resolves `moox-cli` from the repository's `../../bin/moox-cli` before checking `PATH`. It refuses a missing, symlinked, or non-`0600` config.

The Skill archive deliberately does not contain a cross-platform `moox-cli` binary. It depends on that repository binary or an installed `moox-cli` on `PATH`; the wrapper fails clearly when neither exists.

Make each query self-contained in one shell invocation:

```bash
SKILL_ROOT='/absolute/path/resolved-from-the-loaded-SKILL.md'
"$SKILL_ROOT/scripts/data-kline.sh" \
  --data-type crypto \
  --symbol BTC-USDT \
  --interval 1m
```

Never pass `--config`, rely on the CLI's relative default config path, or use `MOOX_SKILL_CONFIG`. Do not read, print, source, or copy the packaged config; it contains derived HMAC credentials.

## Map the request

- Always provide `--data-type`. Map 加密货币 or crypto to `crypto`.
- Add `--exchange` when the user names one. Otherwise omit it so the data type's configured default is used; `crypto` currently defaults to `binance`.
- Pass the user's `--symbol` unchanged. Do not rewrite `BTC-USDT` as `BTCUSDT`, change case, or guess a pair.
- Pass the requested `--interval`; the first catalog contains `1m` only.
- Use `--limit` for a requested count (`1..1000`); otherwise the CLI default is `100`.
- Use RFC3339 `--start-time` and `--end-time` only when the user supplies a time range. The start must not be after the end.
- Use `--timeout` only when the caller needs to override the default RPC timeout. Use `--output` only when a result file was requested.

Examples follow the same replacement rule. Each block is independently executable after resolving its own `SKILL_ROOT`.

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
  --start-time 2026-08-28T00:00:00Z --end-time 2026-08-28T01:00:00Z
```

The CLI resolves `data-type + exchange + interval` by exact lookup in the packaged catalog. Never construct or guess a Space, Dataset, frequency, or series tag. For the initial catalog, `crypto/binance/1m` resolves to `crypto/dataset_binance_spot_kline_1m`, frequency `1m`, and series tag `venue:binance`. Report unsupported catalog combinations as errors rather than falling back to another dataset.

## Handle the result

On success, parse the protobuf JSON response and summarize the resolved data type, exchange, symbol, interval, row count, and returned time range. Preserve the CLI's descending time order. An empty row set is a successful query with no collected data, not an RPC failure.

Never include Gateway secrets, Storage app keys, request signatures, the config contents, or complete authenticated requests in output or diagnostics.
