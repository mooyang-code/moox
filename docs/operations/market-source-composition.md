# 行情源装配与发布边界

## 代码职责

- `internal/runtimecomposition` 创建具体行情源，并注入 Handler、Scheduler、Reconciler。
- `internal/marketfetch` 处理通用采集、路由、存储与 Timer 环境，不导入具体 provider 子包。
- `internal/sources/binance` 持有 Binance 符号编码、HTTP 协议与来源身份；现货为 `spot_http`，永续为 `swap_http`。

市场和产品类型作为请求数据传递，不再由调度层实例化 Binance。现有 `marketdata.Registry` 继续承担来源注册，不增加全局自注册机制。

## 来源切换

初始化规则已显式声明现货、永续的 `source_id`。后续上线必须同步规则、Collector 控制进程和 SCF 包，不应只替换其中一个。

新版永续工厂会拒绝显式的 `spot_http`，而不是把错误来源悄悄解释成永续。上线前检查已存在的规则及 SCF assignment：永续应使用 `swap_http`。需要协调配置更新与函数发布窗口，并在完成后确认 Timer 执行及写入行的 `source_id`；本次代码修改不自动覆盖线上规则或重写历史数据。

Provider 符号压缩与 Timer 恢复共用装配层注入的 codec。不能给不支持该 codec 的旧函数发送省略符号映射的 assignment。

## 本地验证

边界测试覆盖现货/永续、分钟/小时、invoke/Timer 的八种组合，使用本地 TLS HTTP 服务验证 `/api/v3/klines` 与 `/fapi/v1/klines` 的实际路径选择、频率参数及最终来源字段。它们不是交易所线上 E2E；发布后仍需验证真实采集和写入。
