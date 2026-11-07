# OKX 私有接口样例

- `account_push.json`：官方文档 WebSocket account 频道的推送样例原文（https://www.okx.com/docs-v5/en/ ，2026-11-03 抓取），去掉了文档里的行内注释。
- 其他 OKX 样例（orders、positions 推送和 REST）待测试网录制原文后补充（执行计划 D5）。

## 1B 新增样例

- `account_config.json`：`GET /api/v5/account/config` 的官方响应样例原文（https://www.okx.com/docs-v5/en/ ，2026-11-06 抓取）。这份官方样例本身带 `perm=read_only,withdraw,trade` 和 `posMode=long_short_mode`，所以同时覆盖“有提现权限”和“双向持仓”两个阻断项；检测测试里的“正常”“acctLv=1”等情况由这份样例改写 `perm`、`posMode`、`acctLv` 得到，只改这三个字段。
- 未核对：OKX 鉴权类错误码（50100～50114）沿用 `classifyOKXCode` 里已有的集合；官方错误码表不在文档页的同一份 HTML 里，没有逐字核对。50110（IP 不在密钥白名单）按鉴权类处理，也未逐字核对。
