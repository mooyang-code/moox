# Summary

* [项目首页](README.md)
* [前言](前言.md)

---

## 第一部分：整体架构

* [架构总览](架构总览.md)
* [节点服务网关架构](节点服务网关架构.md)
* [大仓架构](大仓架构.md)
* [协议设计](协议设计.md)

## 第二部分：存储引擎

* [量化金融数据概念](量化金融数据概念.md)
* [存储概念与设计意图](存储概念与设计意图.md)
* [存储目标架构与元数据](存储目标架构与元数据.md)
* [存储服务架构与部署](存储服务架构与部署.md)
* [存储引擎架构](存储引擎架构.md)
* [性能基准报告](性能基准报告/存储基准测试-20260620.md)

## 第三部分：管理服务

* [系统初始化](setup.md)
* [认证鉴权](认证鉴权.md)
* [数据库管理](数据库管理.md)
* [云节点管理](云节点管理.md)
* [云节点执行平台架构](云节点执行平台架构.md)
* [代码包管理](代码包管理.md)
* [采集任务管理](采集任务管理.md)
* [内置市场行情采集架构](内置市场行情采集架构.md)
* [行情数据归档模块设计](行情数据归档模块设计.md)
* [主机监控架构设计](主机监控架构设计.md)
* [监控配置](监控配置.md)
* [SCF 短时行情采集架构](architecture/scf-short-lived-market-fetch.md)
* [SCF 定时触发行情采集执行计划](superpowers/plans/2026-08-04-scf-timer-market-fetch.md)

## 运维

* [管理台 HTTPS 与证书](运维/管理台HTTPS与证书.md)
* [Node Gateway 运维手册](ops/node-gateway.md)
* [MooX EventBus 运维](运维/MooX-EventBus运维.md)
* [MooX 指标监控](运维/MooX指标监控.md)
* [数据保留与磁盘空间](运维/数据保留与磁盘空间.md)
* [MooX Trade 运维](运维/MooX-Trade运维.md)
* [子服务健康检查 tRPC 注册改造](superpowers/plans/2026-07-12-health-trpc-registration.md)

## 第四部分：因子与策略

* [Python 计算运行时架构设计](Python计算运行时架构设计.md)
* [Python 运行时详细执行计划](superpowers/plans/2026-07-11-python-runtime.md)
* [因子计算模块设计](因子计算模块设计.md)
* [因子计算模块修改执行计划](superpowers/plans/2026-07-11-factor-runtime-refactor.md)
* [Strategy 交易策略模块架构设计](策略模块架构设计.md)
* [Strategy Python 策略接入手册](策略模块Python策略接入手册.md)
* [MooX 选币策略执行框架设计](选币策略执行框架设计.md)
* [MooX 选币策略执行框架实施计划](superpowers/plans/2026-08-29-moox-coin-selection-strategy.md)
* [策略前端管理台设计](策略前端管理台设计.md)
* [策略前端管理台执行计划](superpowers/plans/2026-07-11-strategy-frontend-console.md)
* [Strategy 交易策略模块执行计划](superpowers/plans/2026-07-11-strategy-module.md)

## 第五部分：交易系统

* [Trade 交易模块功能说明](交易模块功能说明.md)
* [Trade 交易模块架构设计](交易模块架构设计.md)
* [Trade 模块重写执行计划](superpowers/plans/2026-07-11-trade-module-rewrite.md)

## 附录

* [代码审查报告 2026-07-06](代码审查报告-20260706.md)
* [代码审查修复计划 2026-07-06](代码审查修复计划-20260706.md)
