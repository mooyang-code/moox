# MooX Merge

`moox-merge` 是独立的复合 Dataset 合并服务：消费基础 Dataset 行与
`CollectorPeriodCompleted`，按配置把完整输入提交到 mdataset。

它不是 Factor 模块的一部分。Factor 控制面只保存 mdataset 目录；计算引擎消费
`write_kind=input_commit` 的 `DatasetRowsUpserted`。Merge 与二者通过 EventBus 和
Storage 协作，进程互不嵌入。

## Build And Run

```bash
./scripts/build/build.sh merge
./bin/moox-merge -config=config/merge-app.yaml -conf=config/merge-trpc.yaml
```

Linux 交叉编译与部署包：

```bash
./scripts/build/package-merge.sh --output /tmp/moox-merge.zip
```

凭证目录默认 `~/.config/moox/merge`，EventBus role 为 `merge-eventbus`。
