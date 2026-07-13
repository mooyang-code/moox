# SysDeploy Single-Instance Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden SysDeploy as MooX's single-machine, single-instance static service directory by removing endpoint drift, fixing repeated deletion, validating its contract, and showing Monitor health without turning it into a dynamic registry.

**Architecture:** Keep one deployment row per stable `service_name`. Persist only canonical endpoint inputs (`protocol`, `host`, `port`, `gateway_path`) and derive `base_url` and `rpc_address` at response boundaries. Treat `status` as desired configuration state, join Monitor results only in the management UI, and continue routing to the single active endpoint without registration, heartbeats, automatic failover, or load balancing.

**Tech Stack:** Go 1.24, tRPC-Go, Protocol Buffers, GORM, SQLite, Vue 3, TypeScript, Arco Design, existing Monitor APIs.

---

## Product Boundary

This plan deliberately implements a small personal-developer control plane:

- One supported deployment host for persistent MooX services.
- Multiple module processes may run on that host.
- Exactly one current endpoint per `service_name`.
- SCF workers may run remotely, but they consume the one current endpoint map delivered by CloudNode keepalive.
- No self-registration, instance lease, heartbeat registry, automatic endpoint removal, client-side balancing, or same-service multi-instance support.
- `active` means the operator wants the endpoint used. Monitor health is observed state and does not rewrite desired state.

## File Map

- `modules/admin/schema/admin.sql`: simplify `t_service_deployments` to one current row per service and remove persisted derived addresses.
- `modules/admin/internal/service/sysdeploy/model.go`: represent only canonical persisted fields.
- `modules/admin/internal/service/sysdeploy/dao.go`: physically delete current rows and normalize canonical inputs.
- `modules/admin/internal/service/sysdeploy/service.go`: validate the contract and derive response endpoints.
- `modules/admin/proto/sysdeploy_service.proto`: remove unused confirmation fields while preserving derived response fields.
- `modules/admin/proto/admingen/*`: regenerate Admin protobuf and tRPC bindings.
- `modules/admin/internal/service/sysdeploy/service_test.go`: cover endpoint recomputation, validation, and repeated create/delete cycles.
- `web/src/views/settings/service-deployments/index.vue`: stop editing derived fields and join latest Monitor results.
- `web/src/api/admin/sysdeploy.ts`: keep CRUD payloads canonical.
- `web/src/api/admin/types.ts`: distinguish editable deployment inputs from derived response fields.
- `docs/架构总览.md`, `docs/大仓架构.md`, `modules/admin/README.md`, `docs/存储服务架构与部署.md`, `docs/index.md`: document the supported single-machine, single-instance boundary.

### Task 1: Lock The Single-Instance Contract With Tests

**Files:**
- Modify: `modules/admin/internal/service/sysdeploy/service_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`

- [ ] **Step 1: Add a failing address recomputation test**

Create a deployment at `127.0.0.1:18081`, update only the canonical host and port to `127.0.0.2:18082`, and assert all returned representations use the new address:

```go
func TestServiceImpl_UpdateDeployment_RecomputesDerivedAddresses(t *testing.T) {
	db := setupSysDeployTestDB(t)
	svc := NewService(&database.Manager{})
	svc.dao = NewDAO(db)
	ctx := context.Background()

	require.NoError(t, svc.dao.Create(ctx, &Deployment{
		ServiceName: "svc_update_address",
		Protocol:    "http",
		Host:        "127.0.0.1",
		Port:        18081,
		GatewayPath: "trpc.moox.test.Service",
		Status:      "active",
	}))

	rsp, err := svc.UpdateServiceDeployment(ctx, &pb.UpdateServiceDeploymentReq{
		ServiceName: "svc_update_address",
		Deployment: &pb.ServiceDeployment{
			ServiceName: "svc_update_address",
			Protocol:    "http",
			Host:        "127.0.0.2",
			Port:        18082,
			GatewayPath: "trpc.moox.test.Service",
			Status:      "active",
			BaseUrl:     "http://127.0.0.1:18081",
			RpcAddress:  "127.0.0.1:18081",
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "http://127.0.0.2:18082", rsp.GetDeployment().GetBaseUrl())
	assert.Equal(t, "127.0.0.2:18082", rsp.GetDeployment().GetRpcAddress())

	detail, ok := svc.ResolveGatewayServiceDetail(ctx, "svc_update_address")
	require.True(t, ok)
	assert.Equal(t, "127.0.0.2:18082", detail.Address)
}
```

- [ ] **Step 2: Add a failing repeated delete/recreate test**

```go
func TestDAO_DeleteAndRecreate_CanRepeat(t *testing.T) {
	dao := NewDAO(setupSysDeployTestDB(t))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		require.NoError(t, dao.Create(ctx, &Deployment{
			ServiceName: "repeatable",
			Host:        "127.0.0.1",
			Port:        19090,
			Status:      "active",
		}))
		require.NoError(t, dao.Delete(ctx, "repeatable"))
	}
}
```

- [ ] **Step 3: Add table-driven invalid input tests**

Cover port `0`, port `65536`, unsupported protocol, unsupported scope, unsupported status, invalid JSON, unspecified IP `0.0.0.0`, link-local IP, multicast IP, and a hostname. Hostnames are intentionally rejected because the supported deployment contract uses explicit IP addresses.

```go
tests := []struct {
	name string
	item *Deployment
}{
	{"port too large", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 65536}},
	{"unsupported protocol", &Deployment{ServiceName: "svc", Protocol: "udp", Host: "127.0.0.1", Port: 80}},
	{"unsupported scope", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, Scope: "global"}},
	{"unsupported status", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, Status: "healthy"}},
	{"invalid extra JSON", &Deployment{ServiceName: "svc", Protocol: "http", Host: "127.0.0.1", Port: 80, ExtraConfig: "{"}},
	{"hostname unsupported", &Deployment{ServiceName: "svc", Protocol: "http", Host: "service.local", Port: 80}},
}
```

- [ ] **Step 4: Run the focused tests and confirm failure**

Run:

```bash
go test -count=1 ./modules/admin/internal/service/sysdeploy
```

Expected: FAIL on stale derived addresses, the second delete cycle, and validation cases not yet implemented.

- [ ] **Step 5: Commit the contract tests**

```bash
git add modules/admin/internal/service/sysdeploy/service_test.go modules/admin/internal/service/sysdeploy/defaults_test.go
git commit -m "test(admin): define sysdeploy single-instance contract"
```

### Task 2: Make Endpoint Inputs Canonical

**Files:**
- Modify: `modules/admin/schema/admin.sql`
- Modify: `modules/admin/internal/service/sysdeploy/model.go`
- Modify: `modules/admin/internal/service/sysdeploy/dao.go`
- Modify: `modules/admin/internal/service/sysdeploy/service.go`
- Test: `modules/admin/internal/service/sysdeploy/service_test.go`

- [ ] **Step 1: Remove derived address columns from the fresh schema**

Delete `c_base_url` and `c_rpc_address` from `t_service_deployments`. Keep the protobuf response fields so existing consumers still receive both derived forms.

- [ ] **Step 2: Remove `BaseURL` and `RPCAddress` from the GORM model and DAO updates**

`Deployment` must contain only canonical endpoint inputs:

```go
Protocol    string
Host        string
Port        int32
GatewayPath string
```

Remove both derived fields from `DAO.Update` and `normalizeDeployment`.

- [ ] **Step 3: Add pure endpoint derivation helpers**

```go
func deploymentRPCAddress(row *Deployment) string {
	if row == nil || row.Host == "" || row.Port <= 0 {
		return ""
	}
	return net.JoinHostPort(row.Host, strconv.Itoa(int(row.Port)))
}

func deploymentBaseURL(row *Deployment) string {
	if row == nil || (row.Protocol != "http" && row.Protocol != "https") {
		return ""
	}
	return row.Protocol + "://" + deploymentRPCAddress(row)
}
```

Use `net.JoinHostPort` rather than string formatting so a later IPv6-compatible change remains localized.

- [ ] **Step 4: Derive fields in every output path**

Update `modelToPB`, `endpointMap`, `GetServiceDeployments`, and `ResolveGatewayServiceDetail` to call the helpers. Ignore incoming protobuf `base_url` and `rpc_address` in `pbToModel`.

- [ ] **Step 5: Run focused tests**

```bash
go test -count=1 ./modules/admin/internal/service/sysdeploy ./modules/admin/internal/gateway
```

Expected: address recomputation test PASS; repeated deletion and validation tests may still fail until later tasks.

- [ ] **Step 6: Commit canonical endpoint handling**

```bash
git add modules/admin/schema/admin.sql modules/admin/internal/service/sysdeploy
git commit -m "refactor(admin): derive sysdeploy endpoint addresses"
```

### Task 3: Replace Soft Delete With One Current Row

**Files:**
- Modify: `modules/admin/schema/admin.sql`
- Modify: `modules/admin/internal/service/sysdeploy/model.go`
- Modify: `modules/admin/internal/service/sysdeploy/dao.go`
- Modify: `modules/admin/test/admin_host_monitor_cleanup_e2e_test.go`
- Test: `modules/admin/internal/service/sysdeploy/service_test.go`

- [ ] **Step 1: Simplify the table identity**

Remove `c_is_deleted` and replace the composite unique index with:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS idx_service_deployments_name
ON t_service_deployments(c_service_name);
```

This table stores current configuration, not history. Audit history, if later required, belongs in a separate append-only table.

- [ ] **Step 2: Remove `IsDeleted` and soft-delete predicates**

Delete `IsDeleted` from `Deployment`. Remove `c_is_deleted` predicates from `Get`, `List`, `ListActive`, `exists`, update, and default seeding.

- [ ] **Step 3: Physically delete a deployment**

```go
result := d.db.WithContext(ctx).
	Where("c_service_name = ?", serviceName).
	Delete(&Deployment{})
```

Preserve the existing not-found error when `RowsAffected == 0`.

- [ ] **Step 4: Replace legacy retirement with an idempotent physical delete**

`retireLegacyAdminMonitor` should delete only the exact legacy row and remain safe when it is absent.

- [ ] **Step 5: Update cleanup E2E assertions**

Remove direct `c_is_deleted` assertions and assert that querying the retired service name returns zero current rows.

- [ ] **Step 6: Run tests**

```bash
go test -count=1 ./modules/admin/internal/service/sysdeploy ./modules/admin/test
```

Expected: repeated delete/recreate test PASS and cleanup E2E PASS.

- [ ] **Step 7: Commit current-row persistence**

```bash
git add modules/admin/schema/admin.sql modules/admin/internal/service/sysdeploy modules/admin/test/admin_host_monitor_cleanup_e2e_test.go
git commit -m "fix(admin): make sysdeploy deletion repeatable"
```

### Task 4: Enforce The Static Directory Contract

**Files:**
- Modify: `modules/admin/internal/service/sysdeploy/service.go`
- Modify: `modules/admin/proto/sysdeploy_service.proto`
- Regenerate: `modules/admin/proto/admingen/sysdeploy_service.pb.go`
- Regenerate: `modules/admin/proto/admingen/sysdeploy_service.trpc.go`
- Modify: `web/src/api/admin/sysdeploy.ts`
- Test: `modules/admin/internal/service/sysdeploy/service_test.go`
- Test: `modules/admin/internal/service/sysdeploy/rpc/service_test.go`

- [ ] **Step 1: Add strict validation**

Implement these rules after trimming/default normalization:

```text
service_name: required
protocol: http, https, or trpc
host: required IP literal; reject unspecified, multicast, and link-local addresses
port: 1 through 65535
scope: public or internal
status: active or disabled
gateway_path: must not begin with /
extra_config: valid JSON object
```

Use `net.ParseIP`, `ip.IsUnspecified`, `ip.IsMulticast`, `ip.IsLinkLocalUnicast`, `ip.IsLinkLocalMulticast`, and `json.Unmarshal` into `map[string]json.RawMessage`.

- [ ] **Step 2: Remove unused confirmation fields**

Delete `confirm_storage_topology_overlap` from create, update, and delete requests. Warnings remain advisory response data; there is no confirmation gate to imply otherwise.

- [ ] **Step 3: Regenerate Admin bindings**

Run the repository's existing Admin proto generation command from the module documentation or Makefile. Do not manually edit generated descriptors.

- [ ] **Step 4: Keep client payloads canonical**

Define an editable input type that omits derived and server-owned fields:

```ts
export type ServiceDeploymentInput = Pick<ServiceDeployment,
  'service_name' | 'service_kind' | 'protocol' | 'host' | 'port' |
  'gateway_path' | 'scope' | 'status' | 'description' | 'extra_config'
>;
```

Use it in create and update API functions.

- [ ] **Step 5: Run backend and frontend checks**

```bash
go test -count=1 ./modules/admin/internal/service/sysdeploy/...
pnpm --dir web exec vue-tsc --noEmit
```

Expected: all validation and RPC delegation tests PASS; TypeScript reports no payload mismatch.

- [ ] **Step 6: Commit validation and protocol cleanup**

```bash
git add modules/admin/internal/service/sysdeploy modules/admin/proto web/src/api/admin
git commit -m "feat(admin): validate sysdeploy endpoint configuration"
```

### Task 5: Show Desired And Observed Status Separately

**Files:**
- Modify: `web/src/views/settings/service-deployments/index.vue`
- Reuse: `web/src/api/monitor/index.ts`
- Reuse: `web/src/api/monitor/types.ts`

- [ ] **Step 1: Remove editable derived address fields**

Remove the `base_url` and `rpc_address` form controls. Keep one read-only table column called `访问地址`, selecting `base_url || rpc_address || host + ':' + port` from the server response.

- [ ] **Step 2: Load Monitor checks sourced from SysDeploy**

After loading deployment rows, call:

```ts
monitorApi.listChecks({
  space_id: 'moox-system',
  source: 'sysdeploy',
  page: { page: 1, size: 500 },
})
```

Index checks by `check_id`, which is already generated from `service_name` by the Monitor SysDeploy syncer.

- [ ] **Step 3: Load the latest result for visible deployments**

Reuse the established service-monitor pattern with `Promise.allSettled` and `listResults({ check_id, limit: 1 })`. Limit calls to the current table page. A Monitor failure must leave health as `unknown` and must not fail the SysDeploy table load.

- [ ] **Step 4: Present separate state columns**

Rename the existing status column to `配置状态`. Add `健康状态` with:

```text
healthy   latest result success=true
unhealthy latest result success=false
unknown   no check, no result, or Monitor unavailable
```

Show `last_checked_at` or the latest result's `checked_at` in the same cell tooltip. Do not mutate SysDeploy `status` and do not block gateway routing based on this UI join.

- [ ] **Step 5: Run frontend verification**

```bash
pnpm --dir web exec vue-tsc --noEmit
pnpm --dir web run test:unit
```

Expected: typecheck and tests PASS. Manually verify the service deployment page still loads when Monitor is stopped and shows `unknown` rather than an error page.

- [ ] **Step 6: Commit observed health UI**

```bash
git add web/src/views/settings/service-deployments/index.vue web/src/api/admin
git commit -m "feat(web): show sysdeploy service health"
```

### Task 6: Verify Documentation And End-To-End Behavior

**Files:**
- Modify: `docs/架构总览.md`
- Modify: `docs/大仓架构.md`
- Modify: `modules/admin/README.md`
- Modify: `docs/存储服务架构与部署.md`
- Modify: `docs/index.md`
- Modify: `examples/e2e/verify.mjs`

- [ ] **Step 1: Check documentation language**

Verify every current-state document says:

```text
single supported host
multiple processes allowed
one endpoint per service_name
no same-service multi-instance support
no registration, heartbeat, automatic failover, or load balancing
SCF workers consume the static endpoint map and are not persistent service replicas
```

Target-architecture documents may discuss multiple nodes, but must label that content as a future direction.

- [ ] **Step 2: Extend the existing E2E service-deployment flow**

In `examples/e2e/verify.mjs`, add a temporary deployment that:

1. Is created with an IP and port.
2. Is updated to a different IP and port while stale derived response values are supplied.
3. Is read back with newly derived addresses.
4. Is deleted, recreated, and deleted again.
5. Is absent from `ListActiveServiceDeployments` after deletion.

Always delete the temporary row in `finally` so a failed verification does not leave configuration behind.

- [ ] **Step 3: Run all focused verification**

```bash
go test -count=1 ./modules/admin/internal/service/sysdeploy/... ./modules/admin/internal/gateway/... ./modules/admin/test
pnpm --dir web exec vue-tsc --noEmit
pnpm --dir web run test:unit
git diff --check
```

Expected: all commands PASS with no whitespace errors.

- [ ] **Step 4: Run local E2E against a disposable Admin database**

Use the existing `examples/e2e` startup and verification workflow. Do not point the repeated delete test at production data.

- [ ] **Step 5: Commit E2E and final documentation**

```bash
git add docs modules/admin/README.md examples/e2e/verify.mjs
git commit -m "docs: define sysdeploy single-instance deployment model"
```

## Acceptance Criteria

- Changing canonical Host or port changes every returned and routed address immediately.
- A service can be created and deleted repeatedly without uniqueness failures.
- Invalid protocol, IP, port, scope, status, path, and JSON are rejected before persistence.
- There is exactly one current row and one current endpoint for each `service_name`.
- The management page distinguishes desired configuration state from observed Monitor health.
- Monitor unavailability does not prevent viewing or editing service deployment configuration.
- Admin gateway, Collector discovery, CloudNode keepalive, and SCF endpoint payloads retain their existing response shape.
- Documentation consistently states that current MooX supports one host and one instance per service.
- No service registry, heartbeat lease, automatic failover, or load-balancing mechanism is introduced.
