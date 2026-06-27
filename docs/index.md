---
home: true
title: MooX
hero:
  name: MooX
  text: 一站式量化金融数据平台
  tagline: 从数据采集、存储、管理到策略回测的完整闭环
  actions:
    - theme: brand
      text: 开始阅读
      link: /前言
    - theme: alt
      text: GitHub
      link: https://github.com/mooyang-code/moox

features:
  - title: 多源数据采集
    details: 支持交易所（Binance 等）K 线、标的元数据采集，可扩展至行情、舆情等数据源
  - title: 统一存储引擎
    details: 时序数据与记录数据的统一存储与查询，Pebble 主存 + DuckDB OLAP + Bleve 全文索引
  - title: 异步派生视图
    details: 写入主存后异步构建物化视图和全文索引，CQRS 架构保证读写分离
  - title: 管理控制台
    details: 用户管理、权限控制、采集规则配置、包管理、云节点管理、监控
  - title: tRPC 微服务
    details: 基于腾讯 tRPC-Go 框架，统一 HTTP 网关 + PB 协议，前后端分离
  - title: Go Monorepo
    details: go.work 管理 9 个 Module，模块边界清晰，可独立编译和测试

footer: MIT Licensed | Copyright © 2026 mooyang-code
---
