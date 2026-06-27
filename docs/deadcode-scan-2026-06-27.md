# moox 死代码扫描清单

- **扫描时间**：2026-06-27
- **工具**：`staticcheck 2024.1.1 (0.7.0)`，检查项 `U1000`（unused code）
- **范围**：`modules/admin`、`modules/cli`、`modules/collector`、`modules/storage`、`modules/account`、`modules/factor`、`modules/order`、`web-host`
- **已排除噪音**：proto 生成代码（`proto/admingen/`、`proto/gen/`、`*.pb.go`）
- **状态**：已完成部分清理（admin / cli / collector / storage）。清理前后仍保留需确认项会在文末说明。  

## 清理结果（本次更新）

- ✅ 已确认清理：`admin` 的14处、`cli` 的1处、`collector` 的1处、`storage/cmd/moox-storage/main.go`、`storage/internal/infra/device/duckdb/view_store.go` 的15处。  
- ✅ 已确认清理：`storage/internal/services/access` 中 `currentRecordRows`、`view_projection.go` 的记录投影/兼容包装函数、`view_dirty.go` 的 record 脏标记与 record脏构建路径。  
- ⚠️ 保留项：`storage` 中时间序列增量构建相关链路（如 `drainTimeSeriesDirty` + `startViewDirtyTracking`/`stopViewDirtyTracking`）仍保留，未判定为“完全死代码”。  

---

## 1. modules/admin（14 处）

| 文件:行 | 符号 | 类型 |
|---------|------|------|
| `internal/gateway/gateway.go:201` | `(*HTTPRequestHandler).readRequestBody` | method |
| `internal/gateway/gateway.go:334` | `(*HTTPRequestHandler).writeResponse` | method |
| `internal/service/auth/dao/badger.go:17` | `locks` | struct field |
| `internal/service/cloudnode/impl_heartbeat_service.go:299` | `nodeTasksCacheKey` | func |
| `internal/service/cloudnode/impl_heartbeat_service.go:354` | `(*ServiceImpl).getNodeTasksCached` | method |
| `internal/service/cloudnode/impl_heartbeat_service.go:426` | `(*ServiceImpl).loadNodeTasks` | method |
| `internal/service/cloudnode/rpc/helpers.go:86` | `interfaceToStruct` | func |
| `internal/service/cloudnode/service_utils.go:374` | `structToInterface` | func |
| `internal/service/cloudnode/service_utils.go:428` | `maskSecret` | func |
| `internal/service/cloudnode/service_utils.go:473` | `nodeStatusToString` | func |
| `internal/service/collectmgr/impl_task_instance.go:313` | `(*TaskInstanceServiceImpl).tryTransferFailedTask` | method |
| `internal/service/collectmgr/impl_task_instance.go:406` | `(*TaskInstanceServiceImpl).selectNodeWithLoadBalance` | method |
| `internal/service/collectmgr/impl_task_instance.go:416` | `(*TaskInstanceServiceImpl).triggerTaskOnNode` | method |
| `internal/service/collectmgr/impl_task_planner.go:253` | `(*TaskPlannerServiceImpl).selectLeastLoadedNode` | method |

**注意**：`service_utils.go:428` 的 `maskSecret` 与 `rpc/service.go` 中被 `GetCOSAccountInfo`（reveal=false 分支）使用的 `maskSecret` 是**不同包的同名函数**。清理时只删 `service_utils` 包那个，勿误删 `rpc` 包的。

---

## 2. modules/cli（1 处）

| 文件:行 | 符号 | 类型 | 说明 |
|---------|------|------|------|
| `internal/adminclient/client.go:270` | `(*Client).doJSON` | method | 本次 adminclient 重构后改用 `postJSON`，`doJSON` 成遗留死代码 |

---

## 3. modules/collector（1 处）

| 文件:行 | 符号 | 类型 |
|---------|------|------|
| `internal/dnsproxy/ping.go:58` | `pingIP` | func |

---

## 4. modules/storage（约 29 处）

集中在早期 view projection / dirty-tracking 方案遗留，疑似整片被替换后未清理。建议**整块评估**后再删。

### 4.1 cmd/moox-storage/main.go

| 文件:行 | 符号 | 类型 |
|---------|------|------|
| `cmd/moox-storage/main.go:235` | `storageOptions` | func |

### 4.2 internal/infra/device/duckdb/view_store.go（15 个）

| 文件:行 | 符号 |
|---------|------|
| `view_store.go:1139` | `querySubjectSet` |
| `view_store.go:1143` | `querySubjects` |
| `view_store.go:1157` | `timeSeriesRowMatchesQuery` |
| `view_store.go:1176` | `timeSeriesRowMatchesKey` |
| `view_store.go:1205` | `dimensionsEqual` |
| `view_store.go:1217` | `filterRows` |
| `view_store.go:1234` | `rowMatchesFilters` |
| `view_store.go:1247` | `rowMatchesFilter` |
| `view_store.go:1360` | `sortRows` |
| `view_store.go:1419` | `rowColumnValue` |
| `view_store.go:1428` | `compareTypedValues` |
| `view_store.go:1451` | `compareForSort` |
| `view_store.go:1455` | `numericValue` |
| `view_store.go:1459` | `typedValueString` |
| `view_store.go:1463` | `pageRows` |

### 4.3 internal/services/access/

| 文件:行 | 符号 | 类型 |
|---------|------|------|
| `data.go:464` | `(*Service).currentRecordRows` | method |
| `view_dirty.go:60` | `(*Service).markDirtyTimeSeriesKeys` | method |
| `view_dirty.go:79` | `(*Service).markDirtyRecordKeys` | method |
| `view_dirty.go:134` | `(*Service).drainRecordDirty` | method |
| `view_dirty.go:167` | `(*Service).markViewPending` | method |
| `view_dirty.go:180` | `(*Service).dirtyBuildView` | method |
| `view_dirty.go:206` | `(*Service).popRecordDirty` | method |
| `view_dirty.go:252` | `recordDirtyKey` | func |
| `view_projection.go:31` | `(*Service).recordRowsForView` | method |
| `view_projection.go:35` | `(*Service).readRecordProjectionRow` | method |
| `view_projection.go:51` | `isProjectableTimeSeriesView` | func |
| `view_projection.go:55` | `isProjectableRecordView` | func |

---

## 5. 干净模块

`modules/account`、`modules/factor`、`modules/order`、`web-host` —— 无 U1000。

---

## 6. 非 U1000 可疑项（孤立 / 调试遗留）

| 路径 | 性质 | 建议 |
|------|------|------|
| `modules/cli/cmd/probe_svcauth` | 独立 main 包，不在 `build.sh`，硬编码 `http://106.53.107.122:18080` + secret，开发期 service-auth 探测脚本 | 疑似临时调试遗留，建议删除 |
| `modules/storage/cmd/moox-storage-bench` | bench 工具，不在 `build.sh` | 性能基准工具，**可能有意保留**，需确认 |

---

## 7. 清理建议优先级

1. **低风险先做**：`cli/doJSON`（本次重构遗留，确定可删）、`collector/pingIP`、`admin` 14 处（多为早期实现遗留 helper）
2. **中风险**：`probe_svcauth` 调试工具（确认无人用后删）
3. **需整块评估**：`storage` 的 `view_store.go` / `view_dirty.go` / `view_projection.go` 共 29 处——疑似一整套被替换的 view projection/dirty-tracking 方案，删除前确认当前 view 读取链路不再依赖这些函数（建议结合 git history 确认替换点）
4. **保留**：`moox-storage-bench`（除非确认不再做基准测试）

---

## 8. 复扫命令

清理后可用以下命令复扫验证（需 Go 1.25 toolchain，staticcheck 0.7.0）：

```bash
# 单模块（以 admin 为例）
cd modules/admin && GOOS=darwin GOARCH=arm64 go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 -checks U1000 ./... \
  | grep -v "/proto/admingen/\|\.pb\.go\|/proto/gen/"

# storage（CGO 模块，需清空 zig 交叉编译环境）
cd modules/storage && env -u CC -u CXX -u CGO_CFLAGS -u CGO_CXXFLAGS -u CGO_LDFLAGS \
  GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
  go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 -checks U1000 ./... \
  | grep -v "/proto/gen/\|\.pb\.go"
```
