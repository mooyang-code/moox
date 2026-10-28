# CLS 查询脚本与 SearchLog 契约

本文描述仓库 `skills/moox/scripts/cls_search.py` 实际发送的请求。
云端接口的配额、字段上限和错误码属于腾讯云服务端契约，不由 MooX 代码保证；
需要确认时查阅 [腾讯云 SearchLog 文档](https://cloud.tencent.com/document/product/614/56447)。
日常操作见 [CLS 查询与验证](cls-query.md)。

## SDK 与连接

脚本使用 `tencentcloud-sdk-python-cls` 的 `tencentcloud.cls.v20201016.cls_client.ClsClient`，
由 SDK 完成腾讯云请求签名。默认地域 `ap-guangzhou`，默认端点
`cls.internal.tencentcloudapi.com`；可分别通过 `--region` / `CLS_REGION`、
`--endpoint` / `CLS_ENDPOINT` 覆盖。公网端点可显式指定为 `cls.tencentcloudapi.com`。

凭据来自 CLI 参数、`CLS_SECRET_ID` / `CLS_SECRET_KEY` 或 `.env`；`.env` 加载需要可选依赖
`python-dotenv`。仓库不存在 `cls_query.CLSClient` 封装，不能使用旧示例中的该导入路径。

## 脚本发送的字段

| SearchLog 字段 | CLI / 默认行为 |
| --- | --- |
| `TopicId` | 必填 `--topic`，一次只查一个 Topic |
| `Query` | `--query`，默认 `*` |
| `From`、`To` | `--from` / `--since`、`--to` / `--until`，解析成 Unix 毫秒 |
| `SyntaxRule` | `--syntax`，默认 1（CQL），也可为 0（Lucene） |
| `Limit` | `--limit`，默认每页 100 条；大于 1000 时脚本压到 1000，下限由服务端校验 |
| `Sort` | `--sort`，默认 `desc`，也可为 `asc` |
| `HighLight` | `--highlight`，默认 false |
| `UseNewAnalysis` | 默认 true；`--old-analysis` 改为 false |
| `Context` | `--context` 或 `--all` 翻页取得的上次响应值 |

脚本没有提供多 Topic、Offset 或采样率参数。默认只请求一页；`--all` 才自动翻页。
统计分析与日志记录使用不同输出路径，不能把分析响应的 `Results` 为空直接解读为没有匹配日志。

## 可执行示例

先在本机环境配置凭据，再从仓库根目录运行：

```bash
python3 skills/moox/scripts/cls_search.py \
  --topic <TOPIC_ID> --query '*' --since 30m --limit 20 --format json

python3 skills/moox/scripts/cls_search.py \
  --topic <TOPIC_ID> --query '*' --since 1h --all \
  --fields service_name,Msg,Level,Time --format raw --quiet
```

`--format json` 保留完整响应及 `RequestId`；`--format raw` 每行输出一条解析后的 JSON 记录。
先检查原始日志和 Topic 索引，再添加 `service_name` 等字段条件；索引缺失、请求失败和零条结果应分别记录。
