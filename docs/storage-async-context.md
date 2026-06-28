# Storage 异步 Context 约定

RPC 请求触发的异步任务应使用 `trpc.CloneContext(ctx)`，而非 `context.WithoutCancel(ctx)` 或原始请求 ctx。

## 原因

- `trpc.CloneContext` 会 detach 父 ctx 的 deadline/cancel，同时保留 trace/log 关联字段（拷贝 `codec.Msg`）。
- 原始请求 ctx 在 RPC 返回后会被取消，继续用于日志或下游调用会导致 trace 丢失。
- 项目内 admin/collector 模块已统一采用 `trpc.CloneContext` 模式。

## 示例

```go
go func() {
    defer s.asyncWG.Done()
    asyncCtx := trpc.CloneContext(ctx)
    if err := s.rebuildTimeSeriesView(asyncCtx, rebuildReq); err != nil {
        s.reportViewError(asyncCtx, "time_series_view_rebuild", err)
    }
}()
```

## 适用场景

| 场景 | 推荐 |
|------|------|
| RPC 触发的 goroutine | `trpc.CloneContext(ctx)` |
| 服务生命周期 worker（无 RPC ctx） | `trpc.BackgroundContext()` 或 `trpc.CloneContext(runCtx)` |
| 同步 RPC handler | 原始 `ctx` |
