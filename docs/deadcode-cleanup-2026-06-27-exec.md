# dead code 清理执行清单（2026-06-27 二次复扫）

## 一、复扫命令与环境

- 命令：
  - `staticcheck 2024.1.1 (0.7.0)`，检查项 `U1000`（静态未使用代码）。
- 已复跑模块：`modules/admin`、`modules/cli`、`modules/collector`、`modules/storage`。

## 二、复扫结论

- `modules/admin`：通过，无 U1000。
- `modules/cli`：通过，无 U1000。
- `modules/collector`：通过，无 U1000。
- `modules/storage`：通过，无 U1000。

> 说明：上述通过均为执行 `staticcheck -checks U1000 ./...` 后的结果（按开发要求的 Go/平台参数执行）。

## 三、按你的三类拆分的可执行清单

### A. 确认可删（本次可直接批量执行）

1. `modules/admin/internal/service/cloudnode/rpc/helpers.go`  
   - 删除未使用函数 `interfaceToStruct`（已被迁移到 `cloudnode/service_utils.go` 下使用的同名实现）。

2. `modules/admin/internal/service/cloudnode/service_utils.go`  
   - 删除未使用函数 `asMap`。  
   - 这次复查中也顺带修复 `parseTime` 缺失返回路径，避免编译误报。

3. `modules/admin/internal/service/collectmgr/impl_task_instance.go`  
   - 移除未使用导入：`encoding/json`、`.../collectmgr/planner`。

4. `modules/collector/internal/dnsproxy/ping.go`  
   - 移除未使用导入：`fmt`、`net`（当前文件实际仅需要 `context/sort/sync/time/trpc`）。

### B. 仍需人工确认（清单内建议保留/需排查）

1. 目前静态扫描未发现可清理的未决项。  
2. 如需再次核对语义变更历史，可复查旧版 `docs/deadcode-scan-2026-06-27.md` 的“仍需确认”与“清理建议优先级”条目，但这些项均已在新扫描中不再进入 U1000。

### C. 已迁移路径（非真实死代码）

1. admin gateway 旧 `readRequestBody` / `writeResponse` 条目  
   - 当前已使用 `readRequestBodyWithRaw` 替代。

2. storage 旧清单中的 `storageOptions`  
   - 当前实际符号为 `storageOptionsFromConfig`（`modules/storage/cmd/moox-storage/main.go`）。

3. `modules/admin/internal/service/cloudnode/impl_node_service.go` 中 `InvokeFunction` 逻辑已改为调用 `service_utils` 下的 `interfaceToStruct`，与旧 `rpc/helpers.go` 的同名实现职责发生了迁移。

4. `modules/storage/internal/infra/device/duckdb/view_store.go` 中 `buildFilterPredicates` 需要的 `parseFunctionFilter` / `parseSimpleFilter` / `filterValue` 已补齐为本地函数，不再产生编译阻塞。

## 四、建议执行顺序（一次性批量）

1. A 类四项已本次执行。  
2. 其他模块当前无 U1000，可直接形成最终归档结论。

## 五、批次结果快照

- 已完成：`admin`/`cli`/`collector`/`storage` 复扫通过，且本轮清理改动与上述 A 类一致。  
- 待收敛：当前批次无未决项，历史清单可据此归档为“已清理/非真实死代码”。  
