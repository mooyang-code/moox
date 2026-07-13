# 腾讯云 CLS SearchLog API 详细文档

## 基本信息

- **接口名称**: SearchLog
- **接口请求域名**: `cls.tencentcloudapi.com`
- **最后更新时间**: 2025-12-16
- **API 文档**: https://cloud.tencent.com/document/product/614/56447

## 接口限制

| 限制项 | 说明 |
|--------|------|
| 并发限制 | 单个日志主题查询并发不能超过 15 |
| 返回大小 | API 返回数据包最大限制为 49MB |
| 压缩 | 建议在 HTTP Request Header 中启用 gzip 压缩 (`Accept-Encoding: gzip`) |
| 频率限制 | 默认 10000次/秒 |

## 请求方法

- **Method**: `POST`
- **Content-Type**: `application/json`

## 公共参数

除以下参数外，还需传入公共参数：

| 参数 | 说明 |
|------|------|
| Action | 操作名称，本接口为 `SearchLog` |
| Version | API 版本，本接口为 `2020-10-16` |
| Region | 地域，如 `ap-guangzhou` |
| Timestamp | 当前 Unix 时间戳 |
| SecretId | 腾讯云 API 访问密钥 ID |
| Signature | 签名结果 |

## 输入参数

| 参数名 | 必选 | 类型 | 说明 |
|--------|------|------|------|
| TopicId | 否 | String | 日志主题 ID（仅能指定一个），与 Topics 不能同时使用 |
| Topics.N | 否 | Array | 多日志主题列表，最大支持 50 个 |
| From | 是 | Integer | 起始时间，Unix 时间戳（毫秒） |
| To | 是 | Integer | 结束时间，Unix 时间戳（毫秒） |
| Query | 是 | String | 检索分析语句，最大 12KB |
| SyntaxRule | 否 | Integer | 检索语法：0-Lucene，1-CQL（推荐） |
| Sort | 否 | String | 排序方式：`asc` 或 `desc`，默认 `desc` |
| Limit | 否 | Integer | 返回条数，默认 100，最大 1000 |
| Offset | 否 | Integer | 偏移量，不能与 Context 同时使用 |
| Context | 否 | String | 透传上次返回的 Context，用于分页 |
| SamplingRate | 否 | Float | 采样率，0-1 |
| UseNewAnalysis | 否 | Boolean | 使用新的分析格式，建议 true |
| HighLight | 否 | Boolean | 是否高亮关键词 |

## 输出参数

| 参数名 | 类型 | 说明 |
|--------|------|------|
| Context | String | 分页上下文，可用于获取更多日志 |
| ListOver | Boolean | 是否已全部返回 |
| Analysis | Boolean | 是否为统计分析结果 |
| Results | Array | 原始日志列表 |
| ColNames | Array | 列名（旧格式） |
| AnalysisResults | Array | 分析结果（旧格式） |
| AnalysisRecords | Array | 分析结果（新格式） |
| Columns | Array | 列属性（新格式） |
| RequestId | String | 请求 ID |

## 检索语法 (CQL)

### 基础语法

```sql
# 全文检索
"关键词"

# 键值检索
field:value
http_code:200

# 组合检索
field1:value1 AND field2:value2
level:error OR level:warn

# 范围检索
http_code:[400 TO 500]
status>0
```

### 统计分析

```sql
# 分组统计
level:error | SELECT status, count(*) AS count GROUP BY status

# 排序输出
* | SELECT url, count(*) AS pv GROUP BY url ORDER BY pv DESC LIMIT 10

# 多字段分组
* | SELECT year, month, day, count(*) GROUP BY year, month, day

# 聚合函数
* | SELECT avg(response_time), max(response_time), min(response_time)
```

### 常用函数

| 函数 | 说明 | 示例 |
|------|------|------|
| count(*) | 计数 | `SELECT count(*)` |
| avg(field) | 平均值 | `SELECT avg(latency)` |
| max(field) | 最大值 | `SELECT max(cost)` |
| min(field) | 最小值 | `SELECT min(age)` |
| sum(field) | 求和 | `SELECT sum(amount)` |
| percentile | 百分位 | `SELECT percentile(latency, 95)` |

## 错误码

| 错误码 | 说明 |
|--------|------|
| FailedOperation | 操作失败 |
| FailedOperation.InvalidContext | 检索游标已失效 |
| FailedOperation.QueryError | 查询语句运行失败 |
| FailedOperation.SearchTimeout | 查询超时 |
| FailedOperation.SyntaxError | 查询语句解析错误 |
| LimitExceeded.LogSearch | 并发超限（最大值 15） |
| LimitExceeded.SearchResultTooLarge | 返回结果过大 |
| ResourceNotFound.TopicNotExist | 日志主题不存在 |

## Python 调用示例

```python
from cls_query import CLSClient

# 初始化客户端
client = CLSClient(
    secret_id="your_secret_id",
    secret_key="your_secret_key",
    endpoint="cls.tencentcloudapi.com",
    region="ap-guangzhou"
)

# 查询日志
result = client.search_log(
    topic_id="topic-id-xxx",
    query="level:error",
    from_time=1705737600000,
    to_time=1705741200000,
    limit=100,
    sort="desc",
    use_new_analysis=True
)

# 打印结果
print(result)
```

## CQL vs Lucene 语法对比

| 场景 | CQL (推荐) | Lucene |
|------|-----------|--------|
| 精确匹配 | `status:200` | `status:200` |
| 模糊匹配 | `message:*error*` | `message:*error*` |
| 范围 | `latency >= 100` | `latency:[100 TO *]` |
| 逻辑 | `AND OR NOT` | `AND OR NOT` |
| 分组 | `()` | `()` |
| 转义 | 双引号内的特殊字符 | 反斜杠 |
