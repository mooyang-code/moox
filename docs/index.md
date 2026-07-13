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
    details: 当前内置 Binance K 线、标的元数据采集，部署于腾讯云 SCF。采集器按独立 collector 服务组织，后续可扩展更多数据源。
  - icon: 💾
    title: 统一存储引擎
    details: 时序数据与记录数据的统一存储与查询。Pebble 在线事实主存 + DuckDB OLAP 物化视图 + Bleve 全文索引 + Parquet 冷归档。
  - icon: 🔄
    title: 异步派生视图
    details: CQRS 架构，写入主存后通过 MemoryBus/NATS 事件总线异步构建物化视图和全文索引。Blue-Green 模式管理视图版本切换。
  - icon: 🖥️
    title: 管理控制台
    details: 用户管理、JWT 鉴权、服务部署信息、运维监控，以及采集/云节点独立服务的统一 HTTP 网关转发。当前面向个人开发者，正式支持单机多进程、每个服务单实例部署。
  - icon: 🌐
    title: 前端工作台
    details: Vue 3 + Arco Design + Pinia，三套 Axios 实例分层调用 Admin/Storage API。动态路由、Space 上下文切换、CodeMirror/xterm 富交互。
  - icon: 📦
    title: Go Monorepo
    details: go.work 管理多个 Go Module，模块边界清晰，跨模块优先通过 proto 生成代码和网关/RPC 交互。统一 Makefile 构建入口。

---
