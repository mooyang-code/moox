# Storage SearchRows Protocol Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 收窄 `DataSlice.dimensions` 和 `QueryViewColumn` 语义，并把 `SearchText` 改为支持全文和结构化过滤的 `SearchRows`。

**Architecture:** `DataSlice.dimensions` 保留为事实切片定位维度，文档和注释明确它不是普通过滤字段。`QueryViewColumn.expression` 从查询响应协议中移除，表达式只作为 View 元数据内部定义。`SearchRows` 作为 DataSet 维度搜索接口，支持 `text_query`、`filters`、`sorts` 和 `column_names`。

**Tech Stack:** Go, proto3, trpc-open, tRPC-Go, SQLite schema 文档校验。

---

### Task 1: 协议契约测试

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/protocol_contract_test.go`

- [ ] **Step 1: 写失败测试**

在协议契约测试中增加断言：

```go
requireProtocolFileNotContains(t, filepath.Join(root, "proto", "query.proto"), []string{
    "SearchText",
    "TextSearch",
    "expression =",
})
requireProtocolFileContains(t, filepath.Join(root, "proto", "query.proto"), []string{
    "rpc SearchRows",
    "message SearchRowsReq",
    "text_query",
    "repeated common.FilterExpr filters",
    "repeated common.SortSpec sorts",
    "repeated string column_names",
})
requireProtocolFileContains(t, filepath.Join(root, "proto", "data.proto"), []string{
    "参与逻辑定位",
    "不是普通过滤条件",
})
```

- [ ] **Step 2: 运行测试确认失败**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
go test ./internal/services -run TestStorageProtocolUsesCanonicalSurface -count=1
```

Expected: FAIL，提示 `SearchText` 或 `expression` 仍存在，且 `SearchRows` 相关字段未出现。

### Task 2: 更新 proto 与生成代码

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/data.proto`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/query.proto`
- Generated: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto/gen/*.go`

- [ ] **Step 1: 修改 `DataSlice.dimensions` 注释**

将注释改成：

```proto
// dimensions 是参与逻辑定位的低基数业务维度，不是普通过滤条件。
// 只有当某个值决定“是否为同一条事实序列或事实切片”时才放在这里。
map<string, string> dimensions = 4;
```

- [ ] **Step 2: 移除 `QueryViewColumn.expression`**

从 `QueryViewColumn` 删除：

```proto
string expression = 5;
```

并把 `value_type` 字段号调整为 5。

- [ ] **Step 3: 将 `SearchText` 改成 `SearchRows`**

把 `SearchTextReq/SearchTextRsp` 和 RPC 改成：

```proto
message SearchRowsReq {
  common.AuthInfo auth_info = 1;
  string dataset_id = 2;
  string text_query = 3;
  repeated string subject_ids = 4;
  common.TimeRange time_range = 5;
  repeated common.FilterExpr filters = 6;
  repeated common.SortSpec sorts = 7;
  repeated string column_names = 8;
  common.Page page = 9;
}

message SearchRowsRsp {
  common.RetInfo ret_info = 1;
  repeated data.DataRow rows = 2;
  common.PageResult page_result = 3;
}

service QueryService {
  rpc QueryView(QueryViewReq) returns (QueryViewRsp);
  rpc SearchRows(SearchRowsReq) returns (SearchRowsRsp);
}
```

- [ ] **Step 4: 重新生成代码**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
make -C modules/storage/proto clean all
```

Expected: `trpc-open create` 全部 succeed。

### Task 3: 更新服务实现与调用点

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/query.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/internal/services/storage/service_test.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/collector/binance/kline.go`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/collector/internal/collector/binance/symbol.go`

- [ ] **Step 1: 把服务方法改名为 `SearchRows`**

将方法签名改成：

```go
func (s *Service) SearchRows(ctx context.Context, req *pb.SearchRowsReq) (*pb.SearchRowsRsp, error)
```

内部读取 `req.GetTextQuery()`，并把 `req.GetColumnNames()` 传给 `store.ReadRows`。

- [ ] **Step 2: 实现最小结构化过滤**

第一版只支持 `field == value` 风格的 `FilterExpr.expr`：

```text
symbol == $symbol
status == $status
```

实现函数：

```go
func rowMatchesFilters(row *pb.DataRow, filters []*pb.FilterExpr) bool
```

不支持的表达式返回 false，避免误放大查询结果。

- [ ] **Step 3: 更新测试**

在 `service_test.go` 增加 `SearchRows` 测试：写入两行 symbol 资料，使用 `text_query` 命中 `APTUSDT`，再用 `status == $status` 过滤 active。

- [ ] **Step 4: 检查 collector 路径**

确认 Binance collector 仍向 `/trpc.storage.data.DataService/WriteRows` 写入，不再引用旧查询 RPC。

### Task 4: 文档同步与验证

**Files:**
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/pb-protocol-redesign.md`
- Modify: `/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/storage-target-architecture-and-metadata.md`

- [ ] **Step 1: 更新文档**

文档需要明确：

```text
dimensions 是事实切片定位维度，不是普通查询过滤。
表达式列属于 View 元数据和后台构建逻辑，不出现在 QueryViewColumn 响应。
SearchRows 支持 text_query + filters + sorts + column_names。
```

- [ ] **Step 2: 运行校验**

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage
go test ./... -count=1
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test ./modules/cli/... ./modules/collector/... ./modules/control/... ./modules/order/... ./modules/factor/... ./modules/account/...
```

Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "SearchText|TextSearch|expression =" modules/storage/proto modules/storage/internal/services/storage modules/collector/internal/collector/binance docs/pb-protocol-redesign.md docs/storage-target-architecture-and-metadata.md
```

Expected: tests PASS；grep 无旧协议残留。
