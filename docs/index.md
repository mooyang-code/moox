---
home: true
title: MooX
hero:
  name: MooX
  text: 一站式量化金融数据平台
  tagline: 从数据采集、存储、管理到策略回测的完整闭环
  image:
    src: /logo.svg
    alt: MooX
  actions:
    - theme: brand
      text: 开始阅读
      link: /前言
    - theme: alt
      text: 架构总览
      link: /架构总览
    - theme: alt
      text: GitHub
      link: https://github.com/mooyang-code/moox

features:
  - icon: 📥
    title: 多源数据采集
    details: 支持交易所（Binance 等）K 线、标的元数据采集，部署于腾讯云 SCF。插件化采集器架构，已预留 OKX、Twitter、CoinDesk 等数据源扩展点。
  - icon: 💾
    title: 统一存储引擎
    details: 时序数据与记录数据的统一存储与查询。Pebble 在线事实主存 + DuckDB OLAP 物化视图 + Bleve 全文索引 + Parquet 冷归档。
  - icon: 🔄
    title: 异步派生视图
    details: CQRS 架构，写入主存后通过 NATS 事件总线异步构建物化视图和全文索引。Blue-Green 模式管理视图版本切换。
  - icon: 🖥️
    title: 管理控制台
    details: 用户管理、JWT 鉴权、采集规则配置、云节点管理、代码包管理、异步任务编排、监控。统一 HTTP 网关 + tRPC 服务。
  - icon: 🌐
    title: 前端工作台
    details: Vue 3 + Arco Design + Pinia，三套 Axios 实例分层调用 Admin/Storage API。动态路由、Space 上下文切换、CodeMirror/xterm 富交互。
  - icon: 📦
    title: Go Monorepo
    details: go.work 管理 9 个 Go Module，模块边界清晰，跨模块只允许 import proto 生成代码。统一 Makefile 构建入口。

---
