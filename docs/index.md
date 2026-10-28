---
home: true
title: MooX
hero:
  name: MooX
  text: 面向个人的一站式量化平台
  tagline: 行情采集、数据存储、因子计算、策略选股、交易执行与全链路监控
  image:
    src: /logo.svg
    alt: MooX
  actions:
    - theme: brand
      text: 总体设计
      link: /总体设计
    - theme: alt
      text: 构建手册
      link: /构建手册
    - theme: alt
      text: 部署与运维
      link: /部署与运维
    - theme: alt
      text: GitHub
      link: https://github.com/mooyang-code/moox

features:
  - title: 云函数采集
    details: Collector 下发采集计划，执行端通过 WS 推送或 SCF Timer 拉取收盘 K 线并提交到 Storage，缺口由检查与补采修复。
  - title: 字段级事实存储
    details: DataNode 以 Pebble 保存字段级事实并通过 Outbox 发布变更；DuckDB / Bleve View 是可重建的查询索引。
  - title: 因子与策略
    details: 表达式因子按采集周期增量计算并写回 Storage；策略用声明式 DSL 产出组合账户的目标权重。
  - title: 交易执行
    details: Trade 把目标权重换算成订单并逐步收敛，支持实盘、模拟盘和人工干预。
  - title: 事件驱动
    details: 内嵌 NATS JetStream 的 EventBus 连接各模块，事件由统一注册表声明，消费者自己拥有 Consumer。
  - title: 监控与运维
    details: Monitor 汇总进程、主机、数据新鲜度与行情健康，告警推送到企业微信和飞书；moox-cli 负责部署与诊断。
---
