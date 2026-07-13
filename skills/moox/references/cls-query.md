# MooX CLS 查询与验证

使用 `skills/moox/scripts/cls_search.py` 查询 CLS。脚本基于腾讯云 CLS
`SearchLog` API，支持 CQL、相对时间、自动翻页和结构化输出。

## 凭证

凭证按以下顺序读取：

1. `--secret-id` / `--secret-key`
2. `CLS_SECRET_ID` / `CLS_SECRET_KEY`
3. 当前目录或 `skills/moox/.env`

仓库不保存凭证。默认 API 端点是
`cls.internal.tencentcloudapi.com`，地域默认 `ap-guangzhou`。

## 常用查询

```bash
# 最近 1 小时的最近 20 条日志
python3 skills/moox/scripts/cls_search.py \
  --topic <TOPIC_ID> --query '*' --since 1h --limit 20

# 只输出结构化字段，便于脚本处理
python3 skills/moox/scripts/cls_search.py \
  --topic <TOPIC_ID> --query '*' --since 1h \
  --fields service_name,Msg,Level,Caller,Time --format raw --quiet

# 查询错误日志
python3 skills/moox/scripts/cls_search.py \
  --topic <TOPIC_ID> --query 'Level:ERROR' --since 30m

# 统计日志级别
python3 skills/moox/scripts/cls_search.py \
  --topic <TOPIC_ID> \
  --query '* | SELECT Level, count(*) AS count GROUP BY Level'
```

## MooX CLS 验证流程

1. 从本次 `cls-bootstrap.sh` 结果或部署配置取得 Topic ID，不要猜测 Topic ID。
2. 先用 `--query '*'` 拉取少量原始日志，记录 Topic、时间范围、RequestId 和结果数。
3. 检查 `LogJson` 中是否有 `service_name`，并按该字段在客户端筛选服务。
4. 只有确认 CLS Topic 已建立 `service_name` 索引后，才使用
   `service_name:<module>` 作为 CQL 条件；未建索引时 API 会返回
   `field ... is not indexed`，此时不能把“无结果”误判为“没有上报”。
5. 验证字段时同时检查日志时间和服务版本，避免把旧版本日志当成新代码结果。

不要在终端输出 SecretId、SecretKey、签名或完整环境文件内容。查询结果可以包含业务日志，最终报告只保留必要的字段和错误摘要。
