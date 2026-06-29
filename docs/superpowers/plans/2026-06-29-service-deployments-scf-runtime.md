# Service Deployments SCF Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move MooX service endpoint discovery from SCF package-time configuration to runtime service deployment records delivered through control-plane probes.

**Architecture:** Add a control-plane owned service deployment registry as the single source for system service host/port/base URL. Keep storage topology separate from service deployment data, but detect overlapping storage topology endpoints and ask the user to confirm synchronization. During keepalive/probe, admin sends service deployment information to SCF; collector updates runtime config and continues using `/api/service` for control callbacks and direct storage HTTP RPC for storage writes.

**Tech Stack:** Go, tRPC-Go HTTP gateway, GORM/SQLite admin metadata, protobuf JSON, Vue 3, Arco Design, existing MooX admin gateway `/api/admin` and service gateway `/api/service`.

---

## File Structure

`modules/cli/cmd/collector.go`: revert package-time `--storage-url` injection and keep collector packaging environment-agnostic.

`modules/collector/Makefile`: revert `MOOX_STORAGE_URL` package-time rewrite from `build-scf`.

`modules/admin/schema/schema.sql`: add `t_service_deployments` table and indexes in the admin/control-plane schema.

`modules/admin/proto/collect_service.proto`: add `ServiceDeployment` messages and CRUD/list APIs under the existing admin protobuf surface.

`modules/admin/proto/admingen/collect_service.pb.go`: regenerate after proto changes.

`modules/admin/internal/service/sysdeploy/model.go`: define control-plane service deployment model and helpers.

`modules/admin/internal/service/sysdeploy/dao.go`: implement storage and query operations for `t_service_deployments`.

`modules/admin/internal/service/sysdeploy/service.go`: implement validation, upsert, list, and storage-topology impact detection.

`modules/admin/internal/service/sysdeploy/rpc/service.go`: expose service deployment APIs to admin gateway.

`modules/admin/internal/service/cloudnode/keepalive_probe.go`: read service deployment records and include them in keepalive/probe event payloads.

`modules/collector/pkg/model/types.go`: add runtime event fields for `service_deployments`.

`modules/collector/internal/cloudfunction/handler.go`: update runtime config from `service_deployments` during SCF invocation.

`modules/collector/pkg/config/global.go`: add runtime override for storage URL, separate from static package config.

`web/src/api/admin/service-deployments.ts`: add frontend API wrappers through `/api/admin`.

`web/src/api/admin/types.ts`: add frontend types for service deployments and impact warnings.

`web/src/views/settings/service-deployments/index.vue`: add management UI under System Settings.

`web/src/router/route.ts`: register `/settings/service-deployments` route.

`web/src/mock/_data/system_menu.ts`: add menu entry under System Settings.

`web/src/lang/modules/zhCN.ts` and `web/src/lang/modules/enUS.ts`: add menu labels.

`web/src/views/ops/storage/nodes.vue`: add a small notice that service deployment changes can affect storage topology, without moving PrimaryStore topology fields into the service registry.

`skills/debug/references/scf-e2e-debug.md`: update operational docs to remove package-time storage endpoint injection and describe runtime service deployments.

`skills/moox/SKILL.md`: add a guided system initialization workflow that asks where to deploy admin first, then uses the running admin management plane to register and deploy the rest of the services.

---

### Task 1: Revert package-time storage endpoint injection

**Files:**

- Modify: `modules/cli/cmd/collector.go`
- Modify: `modules/cli/cmd/collector_test.go`
- Modify: `modules/collector/Makefile`
- Modify: `skills/debug/references/scf-e2e-debug.md`

- [ ] **Step 1: Remove `StorageURL` from collector package options**

In `modules/cli/cmd/collector.go`, remove this field from `collectorPackageOptions`:

```go
StorageURL string
```

Remove this flag registration from `addCollectorPackageFlags`:

```go
cmd.Flags().StringVar(&opts.StorageURL, "storage-url", "", "moox-storage Access HTTP URL injected into SCF config.yaml system.storage_url; defaults to MOOX_STORAGE_URL")
```

- [ ] **Step 2: Use direct parsed overrides again**

In `packageCollectorFunction`, change the `Overrides` assignment back to:

```go
Overrides: parseCollectorOverrides(opts.Overrides),
```

Delete the entire `collectorPackageOverrides` function.

- [ ] **Step 3: Remove CLI tests for package-time storage injection**

In `modules/cli/cmd/collector_test.go`, remove `storage-url` from the package command and publish command flag assertions.

Delete `TestCollectorPackageOverridesInjectStorageURL`.

- [ ] **Step 4: Remove Makefile package-time storage rewrite**

In `modules/collector/Makefile`, remove the help line:

```make
@echo "    可选: MOOX_STORAGE_URL=http://host:20201 make build-scf v0.0.1"
```

Remove the usage line:

```make
echo "可选: MOOX_STORAGE_URL=http://host:20201 make build-scf v0.0.1"; \
```

Remove the `if [ -n "$$MOOX_STORAGE_URL" ]; then ... fi` block that rewrites `scf-build/config.yaml`.

- [ ] **Step 5: Update SCF debug docs**

In `skills/debug/references/scf-e2e-debug.md`, remove `--storage-url http://<storage-host>:20201` from package/publish examples.

Add this note near the zip inspection section:

```markdown
SCF packages should not hard-code remote storage endpoints. Runtime storage and control endpoints are delivered by the control-plane keepalive/probe event through service deployment records.
```

---

### Task 2: Add admin service deployment schema

**Files:**

- Modify: `modules/admin/schema/schema.sql`
- Create: `modules/admin/internal/service/sysdeploy/model.go`

- [ ] **Step 1: Add table DDL**

Append this table to the admin schema near other system/control-plane tables:

```sql
CREATE TABLE IF NOT EXISTS t_service_deployments (
    c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    c_service_id TEXT NOT NULL,
    c_service_name TEXT NOT NULL,
    c_module TEXT NOT NULL DEFAULT '',
    c_protocol TEXT NOT NULL DEFAULT 'http',
    c_host TEXT NOT NULL DEFAULT '',
    c_port INTEGER NOT NULL DEFAULT 0,
    c_base_url TEXT NOT NULL DEFAULT '',
    c_status TEXT NOT NULL DEFAULT 'active',
    c_config_json TEXT NOT NULL DEFAULT '{}',
    c_attrs_json TEXT NOT NULL DEFAULT '{}',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_port >= 0),
    CHECK (c_status IN ('active', 'disabled', 'deleted')),
    UNIQUE (c_service_id)
);

CREATE INDEX IF NOT EXISTS idx_t_service_deployments_module ON t_service_deployments (c_module, c_status);
CREATE INDEX IF NOT EXISTS idx_t_service_deployments_status ON t_service_deployments (c_status);

CREATE TRIGGER IF NOT EXISTS trg_t_service_deployments_mtime
AFTER UPDATE ON t_service_deployments
FOR EACH ROW
WHEN NEW.c_mtime = OLD.c_mtime
BEGIN
    UPDATE t_service_deployments SET c_mtime = CURRENT_TIMESTAMP WHERE c_id = OLD.c_id;
END;
```

- [ ] **Step 2: Add model**

Create `modules/admin/internal/service/sysdeploy/model.go`:

```go
package sysdeploy

import "strings"

const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusDeleted  = "deleted"
)

const (
	ServiceAdminGateway    = "admin_gateway"
	ServiceStorageAccess   = "storage_access"
	ServiceStorageMetadata = "storage_metadata"
	ServiceStorageView     = "storage_view"
	ServiceWebHost         = "web_host"
	ServiceCollector       = "collector"
	ServiceTrade           = "trade"
)

type ServiceDeployment struct {
	ID          int64  `gorm:"column:c_id;primaryKey"`
	ServiceID   string `gorm:"column:c_service_id"`
	ServiceName string `gorm:"column:c_service_name"`
	Module      string `gorm:"column:c_module"`
	Protocol    string `gorm:"column:c_protocol"`
	Host        string `gorm:"column:c_host"`
	Port        int32  `gorm:"column:c_port"`
	BaseURL     string `gorm:"column:c_base_url"`
	Status      string `gorm:"column:c_status"`
	ConfigJSON  string `gorm:"column:c_config_json"`
	AttrsJSON   string `gorm:"column:c_attrs_json"`
	CreatedAt   string `gorm:"column:c_ctime"`
	UpdatedAt   string `gorm:"column:c_mtime"`
}

func (ServiceDeployment) TableName() string {
	return "t_service_deployments"
}

func NormalizeBaseURL(protocol string, host string, port int32, baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL != "" {
		return baseURL
	}
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		protocol = "http"
	}
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 {
		return ""
	}
	return protocol + "://" + host + ":" + strconv.FormatInt(int64(port), 10)
}
```

- [ ] **Step 3: Fix import after writing model**

Add `strconv` to `model.go` imports:

```go
import (
	"strconv"
	"strings"
)
```

---

### Task 3: Add protobuf API for service deployments

**Files:**

- Modify: `modules/admin/proto/collect_service.proto`
- Generate: `modules/admin/proto/admingen/collect_service.pb.go`

- [ ] **Step 1: Add protobuf messages**

Add these messages near other admin/system management messages:

```proto
message ServiceDeployment {
  int64 id = 1;
  string service_id = 2;
  string service_name = 3;
  string module = 4;
  string protocol = 5;
  string host = 6;
  int32 port = 7;
  string base_url = 8;
  string status = 9;
  string config_json = 10;
  string created_at = 11;
  string updated_at = 12;
  map<string, string> attributes = 13;
}

message ServiceDeploymentImpact {
  string kind = 1;
  string ref_id = 2;
  string ref_name = 3;
  string old_endpoint = 4;
  string new_endpoint = 5;
  string message = 6;
}

message UpsertServiceDeploymentReq {
  ServiceDeployment deployment = 1;
  bool confirm_storage_topology_impact = 2;
}

message UpsertServiceDeploymentRsp {
  RetInfo ret_info = 1;
  ServiceDeployment deployment = 2;
  repeated ServiceDeploymentImpact impacts = 3;
  bool need_confirm = 4;
}

message GetServiceDeploymentReq {
  string service_id = 1;
}

message GetServiceDeploymentRsp {
  RetInfo ret_info = 1;
  ServiceDeployment deployment = 2;
}

message ListServiceDeploymentsReq {
  string module = 1;
  string status = 2;
  Page page = 3;
}

message ListServiceDeploymentsRsp {
  RetInfo ret_info = 1;
  repeated ServiceDeployment deployments = 2;
  PageResult page_result = 3;
}
```

- [ ] **Step 2: Add service RPC methods**

Add methods to the existing admin service definition that is routed by `/api/admin/{service}/{method}`:

```proto
rpc UpsertServiceDeployment(UpsertServiceDeploymentReq) returns (UpsertServiceDeploymentRsp);
rpc GetServiceDeployment(GetServiceDeploymentReq) returns (GetServiceDeploymentRsp);
rpc ListServiceDeployments(ListServiceDeploymentsReq) returns (ListServiceDeploymentsRsp);
```

- [ ] **Step 3: Regenerate protobuf**

Run the repository’s existing proto generation command used for admin protobufs.

Expected result: `modules/admin/proto/admingen/collect_service.pb.go` contains Go structs for the new messages and service methods.

---

### Task 4: Implement service deployment DAO and service

**Files:**

- Create: `modules/admin/internal/service/sysdeploy/dao.go`
- Create: `modules/admin/internal/service/sysdeploy/service.go`
- Test: `modules/admin/internal/service/sysdeploy/service_test.go`

- [ ] **Step 1: Write DAO interface and implementation**

Create `dao.go`:

```go
package sysdeploy

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

type DAO interface {
	Upsert(ctx context.Context, item *ServiceDeployment) (*ServiceDeployment, error)
	Get(ctx context.Context, serviceID string) (*ServiceDeployment, error)
	List(ctx context.Context, module string, status string, offset int, limit int) ([]*ServiceDeployment, int64, error)
}

type daoImpl struct {
	db *gorm.DB
}

func NewDAO(db *gorm.DB) DAO {
	return &daoImpl{db: db}
}

func (d *daoImpl) Upsert(ctx context.Context, item *ServiceDeployment) (*ServiceDeployment, error) {
	if item == nil || item.ServiceID == "" {
		return nil, fmt.Errorf("service_id is required")
	}
	if item.Status == "" {
		item.Status = StatusActive
	}
	if item.Protocol == "" {
		item.Protocol = "http"
	}
	item.BaseURL = NormalizeBaseURL(item.Protocol, item.Host, item.Port, item.BaseURL)
	err := d.db.WithContext(ctx).Where("c_service_id = ?", item.ServiceID).Assign(item).FirstOrCreate(item).Error
	if err != nil {
		return nil, err
	}
	return d.Get(ctx, item.ServiceID)
}

func (d *daoImpl) Get(ctx context.Context, serviceID string) (*ServiceDeployment, error) {
	var item ServiceDeployment
	err := d.db.WithContext(ctx).Where("c_service_id = ? AND c_status != ?", serviceID, StatusDeleted).First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (d *daoImpl) List(ctx context.Context, module string, status string, offset int, limit int) ([]*ServiceDeployment, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	query := d.db.WithContext(ctx).Model(&ServiceDeployment{}).Where("c_status != ?", StatusDeleted)
	if module != "" {
		query = query.Where("c_module = ?", module)
	}
	if status != "" {
		query = query.Where("c_status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []*ServiceDeployment
	if err := query.Order("c_service_id ASC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
```

- [ ] **Step 2: Write service with impact detection contract**

Create `service.go`:

```go
package sysdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type StorageTopologyRef struct {
	Kind        string
	RefID       string
	RefName     string
	OldEndpoint string
	NewEndpoint string
	Message     string
}

type StorageTopologyChecker interface {
	FindEndpointRefs(ctx context.Context, oldBaseURL string, newBaseURL string) ([]StorageTopologyRef, error)
}

type Service struct {
	dao     DAO
	checker StorageTopologyChecker
}

func NewService(dao DAO, checker StorageTopologyChecker) *Service {
	return &Service{dao: dao, checker: checker}
}

func (s *Service) Upsert(ctx context.Context, item *ServiceDeployment, confirmImpact bool) (*ServiceDeployment, []StorageTopologyRef, bool, error) {
	if item == nil {
		return nil, nil, false, fmt.Errorf("deployment is required")
	}
	item.ServiceID = strings.TrimSpace(item.ServiceID)
	item.ServiceName = strings.TrimSpace(item.ServiceName)
	item.Host = strings.TrimSpace(item.Host)
	item.BaseURL = NormalizeBaseURL(item.Protocol, item.Host, item.Port, item.BaseURL)
	if item.ServiceID == "" || item.ServiceName == "" {
		return nil, nil, false, fmt.Errorf("service_id and service_name are required")
	}
	if item.BaseURL == "" {
		return nil, nil, false, fmt.Errorf("base_url or host/port is required")
	}
	impacts, err := s.detectImpact(ctx, item)
	if err != nil {
		return nil, nil, false, err
	}
	if len(impacts) > 0 && !confirmImpact {
		return nil, impacts, true, nil
	}
	created, err := s.dao.Upsert(ctx, item)
	return created, impacts, false, err
}

func (s *Service) detectImpact(ctx context.Context, item *ServiceDeployment) ([]StorageTopologyRef, error) {
	if s.checker == nil {
		return nil, nil
	}
	old, err := s.dao.Get(ctx, item.ServiceID)
	if err != nil {
		return nil, nil
	}
	if old.BaseURL == item.BaseURL {
		return nil, nil
	}
	return s.checker.FindEndpointRefs(ctx, old.BaseURL, item.BaseURL)
}

func AttributesJSON(attrs map[string]string) string {
	if len(attrs) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(attrs)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
```

- [ ] **Step 3: Add service tests**

Create `service_test.go` with tests for these cases:

```go
func TestUpsertRequiresConfirmationWhenStorageTopologyImpacted(t *testing.T)
func TestUpsertSkipsConfirmationWhenNoEndpointChange(t *testing.T)
func TestNormalizeBaseURLBuildsFromHostPort(t *testing.T)
```

Expected behavior: changing `storage_access` from an old URL to a new URL returns `need_confirm=true` and does not persist until `confirm_storage_topology_impact=true`.

---

### Task 5: Wire service deployment RPC into admin service

**Files:**

- Create: `modules/admin/internal/service/sysdeploy/rpc/service.go`
- Modify: admin bootstrap/main registration files for service construction and gateway registration
- Test: `modules/admin/internal/service/sysdeploy/rpc/service_test.go`

- [ ] **Step 1: Add RPC adapter**

Create an RPC service that converts protobuf messages to `sysdeploy.ServiceDeployment` and back.

Required method signatures:

```go
func (s *Service) UpsertServiceDeployment(ctx context.Context, req *pb.UpsertServiceDeploymentReq) (*pb.UpsertServiceDeploymentRsp, error)
func (s *Service) GetServiceDeployment(ctx context.Context, req *pb.GetServiceDeploymentReq) (*pb.GetServiceDeploymentRsp, error)
func (s *Service) ListServiceDeployments(ctx context.Context, req *pb.ListServiceDeploymentsReq) (*pb.ListServiceDeploymentsRsp, error)
```

- [ ] **Step 2: Register service in admin bootstrap**

Register the RPC implementation under service name `sysdeploy`, so the frontend path is:

```text
/api/admin/sysdeploy/UpsertServiceDeployment
/api/admin/sysdeploy/GetServiceDeployment
/api/admin/sysdeploy/ListServiceDeployments
```

Service-to-service access should also work as:

```text
/api/service/sysdeploy/ListServiceDeployments
```

- [ ] **Step 3: Add RPC tests**

Test that `UpsertServiceDeployment` returns `need_confirm=true` when the checker reports topology impacts.

Test that `ListServiceDeployments` returns records with `base_url` populated.

---

### Task 6: Add storage topology impact checker

**Files:**

- Create: `modules/admin/internal/service/sysdeploy/storage_topology_checker.go`
- Modify: admin service construction to provide checker dependency

- [ ] **Step 1: Implement checker interface**

Implement a checker that can query storage metadata for `PrimaryStoreNode.endpoint` values that match the old endpoint.

If admin cannot directly query storage metadata internals, implement this through the existing storage metadata client path already used by admin, or add a small adapter that calls `ListPrimaryStoreNodes` through storage metadata service.

- [ ] **Step 2: Matching rule**

Endpoint matching should compare these normalized forms:

```text
http://host:port
host:port
ip://host:port
trpc://host:port
```

If old deployment is `http://10.0.0.8:20203`, then `PrimaryStoreNode.endpoint` values `10.0.0.8:20203`, `ip://10.0.0.8:20203`, and `trpc://10.0.0.8:20203` count as impacted.

- [ ] **Step 3: Return explicit warning message**

For each match, return:

```text
主存拓扑节点 {node_id} 仍指向旧地址 {old_endpoint}。服务部署地址已变更为 {new_endpoint}，请确认是否需要同步主存拓扑。
```

---

### Task 7: Send service deployments in keepalive/probe events

**Files:**

- Modify: `modules/admin/internal/service/cloudnode/keepalive_probe.go`
- Modify: `modules/admin/internal/service/cloudnode/keepalive_probe_test.go`

- [ ] **Step 1: Add service deployment reader dependency**

Add a small interface in `keepalive_probe.go` or service construction:

```go
type serviceDeploymentLister interface {
	ListActiveServiceDeployments(ctx context.Context) (map[string]ServiceDeploymentEndpoint, error)
}

type ServiceDeploymentEndpoint struct {
	ServiceID string `json:"service_id"`
	Protocol  string `json:"protocol"`
	Host      string `json:"host"`
	Port      int32  `json:"port"`
	BaseURL   string `json:"base_url"`
}
```

- [ ] **Step 2: Include deployments in payload**

Change keepalive event payload to include:

```go
"service_deployments": map[string]ServiceDeploymentEndpoint{
	"admin_gateway": {
		ServiceID: "admin_gateway",
		Protocol: "http",
		Host: serverIP,
		Port: int32(serverPort),
		BaseURL: fmt.Sprintf("http://%s:%d", serverIP, serverPort),
	},
	"storage_access": activeDeployments["storage_access"],
	"storage_metadata": activeDeployments["storage_metadata"],
	"storage_view": activeDeployments["storage_view"],
},
```

- [ ] **Step 3: Keep backward-compatible fields**

Keep these fields for existing SCF compatibility:

```go
"server_ip": serverIP,
"server_port": serverPort,
"moox_server_url": fmt.Sprintf("http://%s:%d", serverIP, serverPort),
"storage_server_url": storageAccessBaseURL,
```

Do not re-add `storage_server_rpc`.

- [ ] **Step 4: Update keepalive tests**

Update `TestBuildKeepaliveEventDataCarriesServerFields` to assert:

```go
deployments, ok := event["service_deployments"].(map[string]interface{})
require.True(t, ok)
require.Contains(t, deployments, "admin_gateway")
require.Contains(t, deployments, "storage_access")
_, hasRPC := event["storage_server_rpc"]
require.False(t, hasRPC)
```

---

### Task 8: Update SCF runtime config from service deployments

**Files:**

- Modify: `modules/collector/pkg/model/types.go`
- Modify: `modules/collector/pkg/config/global.go`
- Modify: `modules/collector/internal/cloudfunction/handler.go`
- Test: `modules/collector/internal/cloudfunction/handler_test.go`

- [ ] **Step 1: Add event deployment types**

In `modules/collector/pkg/model/types.go`, add:

```go
type ServiceDeploymentEndpoint struct {
	ServiceID string `json:"service_id"`
	Protocol  string `json:"protocol"`
	Host      string `json:"host"`
	Port      int32  `json:"port"`
	BaseURL   string `json:"base_url"`
}
```

Add to `CloudFunctionEvent`:

```go
ServiceDeployments map[string]ServiceDeploymentEndpoint `json:"service_deployments,omitempty"`
StorageServerURL   string                               `json:"storage_server_url,omitempty"`
```

- [ ] **Step 2: Add runtime storage URL override**

In `modules/collector/pkg/config/global.go`, add a package-level runtime storage URL protected by mutex:

```go
var runtimeStorageURL string
var runtimeStorageURLMu sync.RWMutex

func UpdateRuntimeStorageURL(rawURL string) {
	runtimeStorageURLMu.Lock()
	defer runtimeStorageURLMu.Unlock()
	runtimeStorageURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
}

func GetRuntimeStorageURL() string {
	runtimeStorageURLMu.RLock()
	defer runtimeStorageURLMu.RUnlock()
	return runtimeStorageURL
}
```

At the start of `GetStorageURL`, before reading `LocalAppConfig`, return `GetRuntimeStorageURL()` if non-empty.

- [ ] **Step 3: Apply deployments in cloudfunction handler**

In `applyRuntimeConfig`, after server info handling, add:

```go
if adminGateway, ok := event.ServiceDeployments["admin_gateway"]; ok {
	if adminGateway.Host != "" && adminGateway.Port > 0 {
		config.UpdateServerInfo(adminGateway.Host, int(adminGateway.Port))
	}
}
if storageAccess, ok := event.ServiceDeployments["storage_access"]; ok {
	if storageAccess.BaseURL != "" {
		config.UpdateRuntimeStorageURL(storageAccess.BaseURL)
	}
} else if event.StorageServerURL != "" {
	config.UpdateRuntimeStorageURL(event.StorageServerURL)
}
```

- [ ] **Step 4: Add SCF runtime tests**

Add tests in `handler_test.go`:

```go
func TestApplyRuntimeConfigUsesServiceDeployments(t *testing.T)
func TestApplyRuntimeConfigFallsBackToStorageServerURL(t *testing.T)
```

Expected behavior: after applying an event with `storage_access.base_url=http://203.0.113.10:20201`, `config.GetStorageURL()` returns that URL.

---

### Task 9: Add frontend service deployment management page

**Files:**

- Create: `web/src/api/admin/service-deployments.ts`
- Modify: `web/src/api/admin/types.ts`
- Create: `web/src/views/settings/service-deployments/index.vue`
- Modify: `web/src/router/route.ts`
- Modify: `web/src/mock/_data/system_menu.ts`
- Modify: `web/src/lang/modules/zhCN.ts`
- Modify: `web/src/lang/modules/enUS.ts`

- [ ] **Step 1: Add frontend types**

Add types:

```ts
export interface ServiceDeployment {
  id?: number;
  service_id: string;
  service_name: string;
  module: string;
  protocol: string;
  host: string;
  port: number;
  base_url: string;
  status: string;
  config_json?: string;
  created_at?: string;
  updated_at?: string;
  attributes?: Record<string, string>;
}

export interface ServiceDeploymentImpact {
  kind: string;
  ref_id: string;
  ref_name: string;
  old_endpoint: string;
  new_endpoint: string;
  message: string;
}
```

- [ ] **Step 2: Add API wrapper**

Create `service-deployments.ts` using existing `callControl`:

```ts
import { callControl } from './http';
import type { Page, PageResult } from '@/api/storage/types';
import type { ServiceDeployment, ServiceDeploymentImpact } from './types';

export function listServiceDeployments(params: { module?: string; status?: string; page?: Page }) {
  return callControl<typeof params, { deployments: ServiceDeployment[]; page_result: PageResult }>('sysdeploy', 'ListServiceDeployments', params);
}

export function upsertServiceDeployment(deployment: ServiceDeployment, confirm_storage_topology_impact = false) {
  return callControl<
    { deployment: ServiceDeployment; confirm_storage_topology_impact: boolean },
    { deployment?: ServiceDeployment; impacts?: ServiceDeploymentImpact[]; need_confirm?: boolean }
  >('sysdeploy', 'UpsertServiceDeployment', { deployment, confirm_storage_topology_impact });
}
```

- [ ] **Step 3: Add page behavior**

The page should show columns:

```text
服务ID, 服务名称, 模块, 协议, Host, Port, Base URL, 状态, 更新时间, 操作
```

The modal should edit:

```text
service_id, service_name, module, protocol, host, port, base_url, status, config_json
```

On save, if response `need_confirm=true`, show confirm modal listing each impact message. If user confirms, call `upsertServiceDeployment(form, true)`.

- [ ] **Step 4: Add route and menu**

Route path:

```text
/settings/service-deployments
```

Menu label:

```text
服务部署
```

English label:

```text
Service Deployments
```

---

### Task 10: Add storage topology page warning

**Files:**

- Modify: `web/src/views/ops/storage/nodes.vue`
- Modify: `web/src/lang/modules/zhCN.ts`

- [ ] **Step 1: Rename page title if approved**

Change title from:

```text
主存节点
```

To:

```text
主存拓扑
```

If the team prefers not to rename now, keep `主存节点` and only add the warning.

- [ ] **Step 2: Add warning banner**

Add an Arco alert above the table:

```vue
<a-alert type="warning" show-icon class="topology-alert">
  主存拓扑用于数据路由；系统服务 IP/端口请在「系统设置 / 服务部署」中维护。修改服务部署后，如地址被主存拓扑引用，需要同步检查这里的 Endpoint。
</a-alert>
```

- [ ] **Step 3: Add CSS spacing**

Add:

```css
.topology-alert {
  margin-bottom: 12px;
}
```

---

### Task 11: Seed default service deployments into the registry

**Files:**

- Modify: admin bootstrap or schema initialization flow
- Test: service deployment seed test under the admin bootstrap/sysdeploy package

- [ ] **Step 1: Add seed source for current deployment**

Do not read `infra/infra.local.yaml` for service deployment data. `t_service_deployments` is the single runtime source of service deployment information, and all later reads/updates must go through the `sysdeploy` service API.

The startup seed is only a bootstrap initializer for missing rows. It must insert the current deployment defaults once, then leave user-managed rows untouched.

Use the current deployment host `106.53.107.122` for public endpoints:

| service_id | service_name | module | protocol | host | port | base_url | attrs_json purpose |
| --- | --- | --- | --- | --- | ---: | --- | --- |
| `admin_gateway` | Admin Gateway | admin | http | `106.53.107.122` | 11000 | `http://106.53.107.122:11000` | public gateway, `/api/admin` and `/api/service` |
| `web_host` | Web Host Static | web | http | `106.53.107.122` | 9527 | `http://106.53.107.122:9527` | public static frontend |
| `storage_metadata` | Storage Metadata HTTP | storage | http | `106.53.107.122` | 20200 | `http://106.53.107.122:20200` | public/direct storage metadata HTTP RPC |
| `storage_access` | Storage Access HTTP | storage | http | `106.53.107.122` | 20201 | `http://106.53.107.122:20201` | public/direct storage access HTTP RPC; SCF writes use this |
| `storage_view` | Storage DataView HTTP | storage | http | `106.53.107.122` | 20202 | `http://106.53.107.122:20202` | public/direct storage view HTTP RPC |
| `storage_metadata_trpc` | Storage Metadata tRPC | storage | trpc | `106.53.107.122` | 20100 | `ip://106.53.107.122:20100` | direct tRPC endpoint |
| `storage_primary_trpc` | Storage PrimaryStore tRPC | storage | trpc | `106.53.107.122` | 20101 | `ip://106.53.107.122:20101` | direct PrimaryStore tRPC endpoint |
| `storage_access_trpc` | Storage Access tRPC | storage | trpc | `106.53.107.122` | 20102 | `ip://106.53.107.122:20102` | direct tRPC endpoint |
| `storage_view_trpc` | Storage DataView tRPC | storage | trpc | `106.53.107.122` | 20103 | `ip://106.53.107.122:20103` | direct tRPC endpoint |
| `admin_auth` | Admin Auth Service | admin | http | `127.0.0.1` | 11100 | `http://127.0.0.1:11100` | internal gateway target |
| `admin_dnsproxy` | Admin DNS Proxy Service | admin | http | `127.0.0.1` | 11101 | `http://127.0.0.1:11101` | internal gateway target |
| `admin_asynctask` | Admin Async Task Service | admin | http | `127.0.0.1` | 11102 | `http://127.0.0.1:11102` | internal gateway target |
| `admin_monitor` | Admin Monitor Service | admin | http | `127.0.0.1` | 11103 | `http://127.0.0.1:11103` | internal gateway target |
| `collectmgr` | Collector Manager Service | admin | http | `127.0.0.1` | 11104 | `http://127.0.0.1:11104` | internal gateway target |
| `cloudnode` | Cloud Node Manager Service | admin | http | `127.0.0.1` | 11105 | `http://127.0.0.1:11105` | internal gateway target |
| `admin_ssh` | SSH Service | admin | http | `127.0.0.1` | 11106 | `http://127.0.0.1:11106` | internal gateway target |
| `admin_space` | Space Manager Service | admin | http | `127.0.0.1` | 11107 | `http://127.0.0.1:11107` | internal gateway target |
| `admin_secret` | Secret Manager Service | admin | http | `127.0.0.1` | 11108 | `http://127.0.0.1:11108` | internal gateway target |
| `trade_account` | Trade Account Service | trade | http | `127.0.0.1` | 11200 | `http://127.0.0.1:11200` | internal gateway target |
| `trade_balance` | Trade Balance Service | trade | http | `127.0.0.1` | 11201 | `http://127.0.0.1:11201` | internal gateway target |
| `trade_fund` | Trade Fund Service | trade | http | `127.0.0.1` | 11202 | `http://127.0.0.1:11202` | internal gateway target |
| `trade_apikey` | Trade API Key Service | trade | http | `127.0.0.1` | 11203 | `http://127.0.0.1:11203` | internal gateway target |
| `trade_channel` | Trade Channel Service | trade | http | `127.0.0.1` | 11204 | `http://127.0.0.1:11204` | internal gateway target |
| `trade_tradeop` | Trade Operation Service | trade | http | `127.0.0.1` | 11205 | `http://127.0.0.1:11205` | internal gateway target |
| `trade_order` | Trade Order Service | trade | http | `127.0.0.1` | 11206 | `http://127.0.0.1:11206` | internal gateway target |
| `trade_tradeq` | Trade Query Service | trade | http | `127.0.0.1` | 11207 | `http://127.0.0.1:11207` | internal gateway target |
| `trade_position` | Trade Position Service | trade | http | `127.0.0.1` | 11208 | `http://127.0.0.1:11208` | internal gateway target |
| `node_exporter` | Node Exporter | ops | http | `106.53.107.122` | 9100 | `http://106.53.107.122:9100` | public/ops monitor target if firewall allows |

- [ ] **Step 2: Implement concrete insert defaults**

Use this exact default slice in the seeding implementation:

```go
defaults := []*sysdeploy.ServiceDeployment{
    {ServiceID: "admin_gateway", ServiceName: "Admin Gateway", Module: "admin", Protocol: "http", Host: "106.53.107.122", Port: 11000, BaseURL: "http://106.53.107.122:11000", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","admin_path":"/api/admin","service_path":"/api/service"}`},
    {ServiceID: "web_host", ServiceName: "Web Host Static", Module: "web", Protocol: "http", Host: "106.53.107.122", Port: 9527, BaseURL: "http://106.53.107.122:9527", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","role":"static"}`},
    {ServiceID: "storage_metadata", ServiceName: "Storage Metadata HTTP", Module: "storage", Protocol: "http", Host: "106.53.107.122", Port: 20200, BaseURL: "http://106.53.107.122:20200", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","trpc_path":"trpc.moox.storage.Metadata"}`},
    {ServiceID: "storage_access", ServiceName: "Storage Access HTTP", Module: "storage", Protocol: "http", Host: "106.53.107.122", Port: 20201, BaseURL: "http://106.53.107.122:20201", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","trpc_path":"trpc.moox.storage.Access","scf_runtime":"storage_url"}`},
    {ServiceID: "storage_view", ServiceName: "Storage DataView HTTP", Module: "storage", Protocol: "http", Host: "106.53.107.122", Port: 20202, BaseURL: "http://106.53.107.122:20202", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","trpc_path":"trpc.moox.storage.DataView"}`},
    {ServiceID: "storage_metadata_trpc", ServiceName: "Storage Metadata tRPC", Module: "storage", Protocol: "trpc", Host: "106.53.107.122", Port: 20100, BaseURL: "ip://106.53.107.122:20100", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","trpc_path":"trpc.moox.storage.Metadata"}`},
    {ServiceID: "storage_primary_trpc", ServiceName: "Storage PrimaryStore tRPC", Module: "storage", Protocol: "trpc", Host: "106.53.107.122", Port: 20101, BaseURL: "ip://106.53.107.122:20101", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","trpc_path":"trpc.moox.storage.PrimaryStore"}`},
    {ServiceID: "storage_access_trpc", ServiceName: "Storage Access tRPC", Module: "storage", Protocol: "trpc", Host: "106.53.107.122", Port: 20102, BaseURL: "ip://106.53.107.122:20102", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","trpc_path":"trpc.moox.storage.Access"}`},
    {ServiceID: "storage_view_trpc", ServiceName: "Storage DataView tRPC", Module: "storage", Protocol: "trpc", Host: "106.53.107.122", Port: 20103, BaseURL: "ip://106.53.107.122:20103", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","trpc_path":"trpc.moox.storage.DataView"}`},
    {ServiceID: "admin_auth", ServiceName: "Admin Auth Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11100, BaseURL: "http://127.0.0.1:11100", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"auth","trpc_path":"trpc.moox.infra.Auth"}`},
    {ServiceID: "admin_dnsproxy", ServiceName: "Admin DNS Proxy Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11101, BaseURL: "http://127.0.0.1:11101", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"dnsproxy","trpc_path":"trpc.moox.infra.Dns"}`},
    {ServiceID: "admin_asynctask", ServiceName: "Admin Async Task Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11102, BaseURL: "http://127.0.0.1:11102", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"asynctask","trpc_path":"trpc.moox.infra.AsyncTask"}`},
    {ServiceID: "admin_monitor", ServiceName: "Admin Monitor Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11103, BaseURL: "http://127.0.0.1:11103", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"monitor","trpc_path":"trpc.moox.ops.Monitor"}`},
    {ServiceID: "collectmgr", ServiceName: "Collector Manager Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11104, BaseURL: "http://127.0.0.1:11104", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"collectmgr","trpc_path":"trpc.moox.collect.CollectMgr"}`},
    {ServiceID: "cloudnode", ServiceName: "Cloud Node Manager Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11105, BaseURL: "http://127.0.0.1:11105", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"cloudnode","trpc_path":"trpc.moox.collect.CloudNodeMgr"}`},
    {ServiceID: "admin_ssh", ServiceName: "SSH Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11106, BaseURL: "http://127.0.0.1:11106", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"ssh","trpc_path":"trpc.moox.ops.Ssh"}`},
    {ServiceID: "admin_space", ServiceName: "Space Manager Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11107, BaseURL: "http://127.0.0.1:11107", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"space","trpc_path":"trpc.moox.admin.SpaceMgr"}`},
    {ServiceID: "admin_secret", ServiceName: "Secret Manager Service", Module: "admin", Protocol: "http", Host: "127.0.0.1", Port: 11108, BaseURL: "http://127.0.0.1:11108", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"secret","trpc_path":"trpc.moox.ops.SecretMgr"}`},
    {ServiceID: "trade_account", ServiceName: "Trade Account Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11200, BaseURL: "http://127.0.0.1:11200", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_account","trpc_path":"trpc.moox.trade.AccountSvc"}`},
    {ServiceID: "trade_balance", ServiceName: "Trade Balance Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11201, BaseURL: "http://127.0.0.1:11201", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_balance","trpc_path":"trpc.moox.trade.BalanceSvc"}`},
    {ServiceID: "trade_fund", ServiceName: "Trade Fund Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11202, BaseURL: "http://127.0.0.1:11202", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_fund","trpc_path":"trpc.moox.trade.FundSvc"}`},
    {ServiceID: "trade_apikey", ServiceName: "Trade API Key Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11203, BaseURL: "http://127.0.0.1:11203", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_apikey","trpc_path":"trpc.moox.trade.ApiKeySvc"}`},
    {ServiceID: "trade_channel", ServiceName: "Trade Channel Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11204, BaseURL: "http://127.0.0.1:11204", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_channel","trpc_path":"trpc.moox.trade.ChannelSvc"}`},
    {ServiceID: "trade_tradeop", ServiceName: "Trade Operation Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11205, BaseURL: "http://127.0.0.1:11205", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_tradeop","trpc_path":"trpc.moox.trade.TradeOpSvc"}`},
    {ServiceID: "trade_order", ServiceName: "Trade Order Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11206, BaseURL: "http://127.0.0.1:11206", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_order","trpc_path":"trpc.moox.trade.OrderSvc"}`},
    {ServiceID: "trade_tradeq", ServiceName: "Trade Query Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11207, BaseURL: "http://127.0.0.1:11207", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_tradeq","trpc_path":"trpc.moox.trade.TradeQuerySvc"}`},
    {ServiceID: "trade_position", ServiceName: "Trade Position Service", Module: "trade", Protocol: "http", Host: "127.0.0.1", Port: 11208, BaseURL: "http://127.0.0.1:11208", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"internal","gateway_service_id":"trade_position","trpc_path":"trpc.moox.trade.PositionSvc"}`},
    {ServiceID: "node_exporter", ServiceName: "Node Exporter", Module: "ops", Protocol: "http", Host: "106.53.107.122", Port: 9100, BaseURL: "http://106.53.107.122:9100", Status: sysdeploy.StatusActive, ConfigJSON: "{}", AttrsJSON: `{"visibility":"public","role":"metrics"}`},
}
```

- [ ] **Step 3: Keep bootstrap local config separate from deployment registry**

Admin still needs local startup config for its own process bootstrap, such as database path and listener ports in `trpc_go.yaml`. That bootstrap config must not be treated as service deployment discovery data.

Runtime consumers must use `t_service_deployments` through `sysdeploy`:

- Cloudnode keepalive/probe reads service deployments from `sysdeploy`, not `pkg/infraconfig`.
- SCF receives `service_deployments` in the event payload, not package-time config.
- Frontend reads and updates service deployment data through `/api/admin/sysdeploy/*`.
- Backend service-to-service callers may read through `/api/service/sysdeploy/*` when needed.

Internal gateway target rows should keep `127.0.0.1`, because those ports are same-host targets used by admin gateway forwarding.

- [ ] **Step 4: Do not overwrite user changes**

Seeding only inserts missing `service_id`; it must not update existing rows.

- [ ] **Step 5: Add seed test**

Add a test that starts with an empty admin DB and asserts that seeding creates at least these public rows:

```go
admin_gateway:      http://106.53.107.122:11000
web_host:           http://106.53.107.122:9527
storage_metadata:   http://106.53.107.122:20200
storage_access:     http://106.53.107.122:20201
storage_view:       http://106.53.107.122:20202
node_exporter:      http://106.53.107.122:9100
```

Also assert that internal gateway target rows such as `collectmgr` and `cloudnode` use `127.0.0.1`, not the public host.

- [ ] **Step 6: Deprecate infra service endpoint usage in this flow**

Remove service deployment endpoint reads from the SCF/probe flow. Do not call these helpers from new sysdeploy code:

```go
infraconfig.AdminGateway()
infraconfig.WebHost()
infraconfig.StorageAccessURL()
infraconfig.XDataURL()
```

If those helpers are still used by unrelated deploy scripts or legacy code, leave them for a separate cleanup. This feature must not rely on them for runtime service discovery.

---

### Task 12: Validation checklist

**Files:**

- No new files unless test fixes are needed.

- [ ] **Step 1: Run focused admin tests**

Run:

```bash
cd modules/admin && go test ./internal/service/sysdeploy/... ./internal/service/cloudnode/...
```

Expected: PASS.

- [ ] **Step 2: Run focused collector tests**

Run:

```bash
cd modules/collector && go test ./internal/cloudfunction/... ./pkg/config/...
```

Expected: PASS.

- [ ] **Step 3: Run frontend type check**

Run:

```bash
cd web && ./node_modules/.bin/vue-tsc --noEmit
```

Expected: PASS.

- [ ] **Step 4: Manual acceptance**

Create or update `storage_access` in `/#/settings/service-deployments`.

Expected: If the old endpoint is referenced by `/#/ops/storage/nodes`, the UI shows an impact confirmation before save.

Trigger a cloudnode keepalive probe.

Expected: SCF event contains `service_deployments.storage_access.base_url`, collector logs show storage writes using direct storage URL, and control callbacks still use `/api/service` with `Auth` header.

---

## Execution Notes

The first implementation step must revert the package-time `--storage-url` changes from the previous iteration. The final design should not require SCF packages to contain environment-specific storage host/port values.

The service deployment registry is the source for runtime discovery. Storage topology remains the source for storage routing. Overlap is handled through impact detection and explicit user confirmation, not silent automatic synchronization.

---

### Task 13: Add guided system initialization workflow to the MooX skill

**Files:**

- Modify: `skills/moox/SKILL.md`
- Optional create: `skills/moox/references/system-initialization.md`

- [ ] **Step 1: Add initialization trigger guidance**

In `skills/moox/SKILL.md`, update the description or body so the skill clearly applies when the user asks for:

```text
初始化 MooX
初始化系统
全新部署 MooX
部署 admin 管理台
部署其他服务
配置服务部署信息
```

The skill must guide the user through an ordered initialization flow instead of asking for all service targets at once.

- [ ] **Step 2: Define the two-phase initialization flow**

Add this section to the skill body or to `skills/moox/references/system-initialization.md`:

```markdown
## System Initialization Workflow

When initializing a new MooX deployment, avoid asking for every service target upfront. Bootstrap the control plane first, then use it as the source of truth for the rest of the system.

### Phase 1: Bootstrap admin management plane

Ask the user where to deploy the admin management plane:

- SSH target, for example `ubuntu@106.53.107.122`
- deploy directory, for example `/home/ubuntu/moox`
- public host/IP for browser and SCF access, for example `106.53.107.122`
- admin gateway port, default `11000`
- web-host/static frontend port, default `9527`

Deploy only the minimum management stack first:

- `moox-admin`
- web frontend assets
- `moox-web-host`
- scripts/config needed to start those services

After deployment, verify admin gateway and web-host are reachable before asking about other services.

### Phase 2: Register and deploy remaining services

After admin is reachable, ask the user where to deploy each remaining service group:

- storage HTTP services: Metadata `20200`, Access `20201`, DataView `20202`
- storage tRPC services: Metadata `20100`, PrimaryStore `20101`, Access `20102`, DataView `20103`
- trade services: `11200-11208`
- collector/SCF runtime endpoints if applicable
- ops endpoints such as node_exporter `9100`

Write the answers into `t_service_deployments` through `/api/admin/sysdeploy/*`. Do not write service deployment information into `infra/infra.local.yaml`.

Then deploy or restart each service group. After each deployment succeeds, update the corresponding `t_service_deployments` row if the host, port, protocol, or base URL changed.
```

- [ ] **Step 3: Add explicit interaction order**

Add this exact behavior to the skill:

```markdown
For system initialization, ask questions in this order:

1. Ask only for the admin deployment target first.
2. Deploy admin backend and web frontend first.
3. Confirm admin gateway `/api/admin/health` and web-host are reachable.
4. Only then ask where storage, trade, collector, and ops services should be deployed.
5. Store those service deployment answers through the sysdeploy API.
6. Use service deployment records for SCF probe payloads and runtime discovery.
```

- [ ] **Step 4: Clarify separation from storage topology**

Add this note:

```markdown
Service deployment records are not storage topology records. If a storage service endpoint changes, check whether PrimaryStore topology endpoints under `/#/ops/storage/nodes` still reference the old address. Warn the user and ask before synchronizing topology endpoints.
```

- [ ] **Step 5: Add example prompt response**

Add a short example so future agents follow the intended interaction:

```markdown
User: 初始化 MooX 到一台新机器

Assistant should ask first:
请先告诉我 admin 管理台要部署到哪里：SSH 目标、部署目录、公网 IP、admin 网关端口、web-host 端口。我们先把 admin 后端和前端跑起来；成功后，我再继续询问 storage/trade/collector/ops 服务分别部署到哪里，并写入服务部署表。
```

- [ ] **Step 6: Do not add evals yet**

This change is an operational workflow addition. Do not run skill evals during this implementation unless the user explicitly asks to evaluate the skill behavior. Keep the skill update focused and small.
