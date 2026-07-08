# moox-factor

因子计算模块（**早期占位**）。当前 `moox-factor` 二进制仅输出 health JSON，尚未接入 Storage 或独立 RPC 服务。

## 现状

```bash
# 仓库根目录
./scripts/build.sh factor

# 模块目录
go run ./cmd/moox-factor
# {"module":"factor","ready":true}
```

## 目录结构

```text
cmd/moox-factor/main.go
internal/service/service.go   占位 Health 响应
```

## 规划方向

因子定义与结果列已可在 Storage 元数据（`Factor`、`DatasetColumn.origin_type=factor`）中登记；本模块后续可承担：

- 因子任务调度与参数化计算
- 结果写回 Storage Access（列级更新）
- 与 View 物化链路集成

详细存储模型见 [docs/存储概念与设计意图.md](../../docs/存储概念与设计意图.md)；模块整体落地方案见 [docs/因子计算模块设计.md](../../docs/因子计算模块设计.md)。

## 相关模块

- [storage](../storage/) — 因子结果持久化与查询
- [trade](../trade/) — 交易域（独立）
