# 默认初始化配置

这个目录是 `moox-cli setup init --config-dir` 的默认输入，只识别三个固定文件：

| 文件 | 消费方 | 内容 |
| --- | --- | --- |
| `metadata.yaml` | `moox-cli setup init` | Admin 业务 Space 与 Storage 元数据 |
| `dataset-health-policy.yaml` | Monitor | 实时 Dataset 健康阈值 |
| `service-deployments.yaml` | Admin 部署导入 | 默认服务和 RPC 端点 |

`metadata.yaml` 的业务 Space 只有 `stock_cn` 和 `crypto_market`。`moox_system` 带
`attributes.scope: internal`，只进入 Storage，不显示在管理台业务空间选择器中。

初始化可重复执行。已有资源与声明一致时报告 unchanged；契约不一致时失败，不覆盖已有
配置和数据。Dataset 先以 disabled 创建，通过 Storage 激活检查后由 `setup init`
显式激活。

```bash
moox-cli setup init \
  --file ./custom.toml \
  --config-dir ./examples/setup/default \
  --storage-host control
```

`--storage-host` 必须是 `custom.toml` 中已经部署 Storage 的主机名。命令只读取
`metadata.yaml` 完成初始化；另外两份文件分别由 Monitor 和 Admin 部署导入读取。
