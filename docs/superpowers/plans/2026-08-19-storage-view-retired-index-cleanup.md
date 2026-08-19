# Storage View Retired Index Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-rebuild and startup View-index deletion with one safe tRPC Timer job that removes continuously unreferenced DuckDB and Bleve indexes after 60 seconds.

**Architecture:** Storage View engines expose a read-only managed-index inventory, while `view.Service` owns a generation-aware in-memory candidate state machine. A tRPC Timer service runs the sweep every 30 seconds through `timerjob.Job`; each destructive pass requires a complete Metadata snapshot, local runtime revalidation, a matching physical generation, and the per-index gate. A/B activation only marks the previous slot as retiring and never starts a cleanup goroutine.

**Tech Stack:** Go 1.23, tRPC-Go Timer (`trpc-database/timer`), `packages/timerjob`, DuckDB, Bleve, protobuf Metadata client, Go unit/race tests, shell deployment contracts.

---

## File Map

- Create `modules/storage/internal/service/view/index_cleanup.go`: candidate state, protected-set discovery, generation-safe sweep, and cleanup logging.
- Create `modules/storage/internal/service/view/index_cleanup_test.go`: deterministic fake-clock and fake-engine cleanup state-machine tests.
- Create `modules/storage/internal/bootstrap/view_index_cleanup_timer.go`: tRPC Timer registration through `timerjob.Job`.
- Create `modules/storage/internal/bootstrap/view_index_cleanup_timer_test.go`: registration constants and missing-service fail-fast tests.
- Modify `modules/storage/internal/service/viewindex/model.go`: optional managed-index listing contract.
- Modify `modules/storage/internal/service/viewindex/slots.go`: reject non-canonical A/B slot IDs before filesystem cleanup.
- Modify `modules/storage/internal/service/viewindex/duckdb/index_manager.go`: enumerate official DuckDB View index IDs within its root.
- Modify `modules/storage/internal/service/viewindex/duckdb/index_manager_test.go`: DuckDB inventory and database/WAL removal coverage.
- Modify `modules/storage/internal/service/viewindex/bleve/index.go`: enumerate official Bleve View index directories within its root.
- Modify `modules/storage/internal/service/viewindex/bleve/index_test.go`: Bleve inventory and directory removal coverage.
- Modify `modules/storage/internal/service/view/service.go`: generation-aware retirement state and gate-safe prepare/removal helpers.
- Modify `modules/storage/internal/service/view/build.go`: stop launching delayed deletion goroutines after a switch.
- Modify `modules/storage/internal/service/view/reconcile.go`: stop describing `Grace` as a deletion scheduler and use retirement-only switching.
- Modify `modules/storage/internal/service/view/service_test.go`: prove the active index remains queryable while old-index cleanup is pending.
- Modify `modules/storage/cmd/server/main.go`: register the cleanup Timer before serving.
- Modify `modules/storage/config/storage_view/trpc_go.yaml`: declare the 30-second cleanup Timer service on port `20308` with a 20-second tRPC timeout.
- Modify `scripts/tests/contract/test-deploy-moox-storage-view.sh`: assert the Timer service is packaged in the production Storage View config.

### Task 1: Add Engine-Owned Managed Index Discovery

**Files:**
- Modify: `modules/storage/internal/service/viewindex/model.go`
- Modify: `modules/storage/internal/service/viewindex/slots.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager_test.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index_test.go`

- [ ] **Step 1: Add failing inventory tests for DuckDB**

Add a test that creates two official A/B databases, adjacent WAL and prepare artifacts, and malformed files. The inventory must return only the two parsed official IDs:

```go
func TestListManagedIndexesReturnsOnlyOfficialDuckDBFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	ids := []string{
		viewindex.ViewIndexID("space", "prices", viewindex.SlotA),
		viewindex.ViewIndexID("space", "prices", viewindex.SlotB),
	}
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(root, id+".duckdb"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, id+".duckdb.wal"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"junk.duckdb", ids[0] + ".duckdb.prepare-123", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := manager.ListManagedIndexes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	slices.Sort(ids)
	if !slices.Equal(got, ids) {
		t.Fatalf("managed indexes = %v, want %v", got, ids)
	}
}
```

- [ ] **Step 2: Add failing inventory tests for Bleve**

Add the same boundary for directories. A managed directory must have an official A/B ID; files, `.prepare-*`, and malformed directories are ignored:

```go
func TestListManagedIndexesReturnsOnlyOfficialBleveDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bleve")
	index, err := Open(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()

	ids := []string{
		viewindex.ViewIndexID("space", "records", viewindex.SlotA),
		viewindex.ViewIndexID("space", "records", viewindex.SlotB),
	}
	for _, id := range ids {
		if err := os.Mkdir(filepath.Join(root, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, ids[0]+".prepare-123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, viewindex.ViewIndexID("space", "file", viewindex.SlotA)), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := index.ListManagedIndexes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	slices.Sort(ids)
	if !slices.Equal(got, ids) {
		t.Fatalf("managed indexes = %v, want %v", got, ids)
	}
}
```

- [ ] **Step 3: Run the focused tests and verify they fail**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test ./internal/service/viewindex/duckdb ./internal/service/viewindex/bleve -run 'TestListManagedIndexes' -count=1
```

Expected: compilation fails because `ListManagedIndexes` is not defined.

- [ ] **Step 4: Add the optional discovery interface**

Append to `viewindex/model.go`:

```go
// ManagedIndexLister returns only official physical index IDs owned by this
// engine. Callers may remove returned IDs through Engine.Remove; filesystem
// paths never cross this boundary.
type ManagedIndexLister interface {
	ListManagedIndexes(context.Context) ([]string, error)
}
```

- [ ] **Step 5: Implement bounded DuckDB inventory**

Add this method to `duckdb/index_manager.go`:

```go
func (m *IndexManager) ListManagedIndexes(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, fmt.Errorf("list DuckDB view indexes: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".duckdb") || strings.Contains(entry.Name(), ".prepare-") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".duckdb")
		if _, err := viewindex.ParseViewIndexID(id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
```

Use the existing `viewindex` import and add `sort` if the file does not already import it.

Before relying on `ParseViewIndexID`, make it reject any slot other than the canonical lowercase `a` or `b`, and require a `ViewIndexID` round trip. This prevents names such as `view_s..._v..._z.duckdb` from entering the deletion set.

- [ ] **Step 6: Implement bounded Bleve inventory**

Add this method to `bleve/index.go`:

```go
func (i *Index) ListManagedIndexes(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(i.root)
	if err != nil {
		return nil, fmt.Errorf("list Bleve view indexes: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || strings.Contains(entry.Name(), ".prepare-") {
			continue
		}
		if _, err := viewindex.ParseViewIndexID(entry.Name()); err != nil {
			continue
		}
		ids = append(ids, entry.Name())
	}
	sort.Strings(ids)
	return ids, nil
}
```

- [ ] **Step 7: Prove engine removal deletes the complete artifact**

Extend the engine tests so DuckDB `Remove` deletes both `<id>.duckdb` and `<id>.duckdb.wal`, while Bleve `Remove` deletes the entire official directory. The assertions must use `errors.Is(err, os.ErrNotExist)` after removal.

- [ ] **Step 8: Run and pass both engine suites**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test ./internal/service/viewindex/duckdb ./internal/service/viewindex/bleve -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit the engine boundary**

```bash
git add modules/storage/internal/service/viewindex/model.go \
  modules/storage/internal/service/viewindex/duckdb/index_manager.go \
  modules/storage/internal/service/viewindex/duckdb/index_manager_test.go \
  modules/storage/internal/service/viewindex/bleve/index.go \
  modules/storage/internal/service/viewindex/bleve/index_test.go
git commit -m "feat(storage): list managed view indexes"
```

### Task 2: Implement the Generation-Safe Cleanup State Machine

**Files:**
- Create: `modules/storage/internal/service/view/index_cleanup.go`
- Create: `modules/storage/internal/service/view/index_cleanup_test.go`
- Modify: `modules/storage/internal/service/view/service.go`

- [ ] **Step 1: Define deterministic cleanup test doubles**

Create `index_cleanup_test.go` with a fake engine that implements `viewindex.Engine` and `viewindex.ManagedIndexLister`, records removals, and can fail one removal. Use official IDs from `viewindex.ViewIndexID`. Add a `cleanupMetadata` wrapper around the existing `reconcileMetadata` fake that can return all Views or a transport error.

The table must cover these exact scenarios:

```go
func TestCleanupRetiredIndexesRequiresContinuousUnreferencedAge(t *testing.T)
func TestCleanupRetiredIndexesProtectsMetadataAndRuntimeIndexes(t *testing.T)
func TestCleanupRetiredIndexesMetadataFailureDeletesNothing(t *testing.T)
func TestCleanupRetiredIndexesRetriesRemoveFailure(t *testing.T)
func TestCleanupRetiredIndexesCancelsReusedGeneration(t *testing.T)
func TestCleanupRetiredIndexesRediscoveryAfterRestart(t *testing.T)
```

Use a mutable clock so the first sweep at `T0` discovers only, `T0+59s` retains, and `T0+60s` removes:

```go
now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
opts := RetiredIndexCleanupOptions{
	Metadata: metadata,
	MinUnreferencedAge: time.Minute,
	Now: func() time.Time { return now },
}
```

- [ ] **Step 2: Run the new tests and verify they fail**

Run:

```bash
cd modules/storage
go test ./internal/service/view -run 'TestCleanupRetiredIndexes' -count=1
```

Expected: compilation fails because `RetiredIndexCleanupOptions` and `CleanupRetiredIndexes` do not exist.

- [ ] **Step 3: Make retirement generation-aware**

In `service.go`, replace:

```go
retiringIndexes map[string]struct{}
```

with:

```go
retiringIndexes map[string]uint64
cleanupCandidates map[managedIndexRef]retiredIndexCandidate
```

Initialize both maps in `New`. Replace the retirement helpers with conditional generation-aware forms:

```go
func (s *Service) markIndexRetiring(id string, generation uint64) {
	if id == "" {
		return
	}
	s.mu.Lock()
	s.retiringIndexes[id] = generation
	s.mu.Unlock()
}

func (s *Service) clearIndexRetiring(id string, generation uint64) {
	s.mu.Lock()
	if current, ok := s.retiringIndexes[id]; ok && current == generation {
		delete(s.retiringIndexes, id)
	}
	s.mu.Unlock()
}

func (s *Service) retiringGeneration(id string) (uint64, bool) {
	s.mu.RLock()
	generation, ok := s.retiringIndexes[id]
	s.mu.RUnlock()
	return generation, ok
}
```

- [ ] **Step 4: Define the cleanup types and protected-set reader**

Create `index_cleanup.go` with these types:

```go
const defaultRetiredIndexAge = time.Minute

type managedIndexRef struct {
	engine string
	indexID string
}

type retiredIndexCandidate struct {
	firstSeen time.Time
	lastSeen time.Time
	generation uint64
}

type RetiredIndexCleanupOptions struct {
	Metadata MetadataClient
	MinUnreferencedAge time.Duration
	Now func() time.Time
}
```

Implement `protectedIndexes` by paginating `ListViews` with `Status: ""`, adding every non-empty `ActiveIndexId` and every non-empty `IndexBuild.IndexId`. After Metadata succeeds, add every local runtime `active` and `next` under each runtime lock. A nil response, non-success `RetInfo`, transport error, or pagination error returns an error and no partial set.

- [ ] **Step 5: Implement discovery without first-run deletion**

Implement the public sweep:

```go
func (s *Service) CleanupRetiredIndexes(ctx context.Context, opts RetiredIndexCleanupOptions) error
```

It must:

1. Validate `opts.Metadata`.
2. Default `MinUnreferencedAge` to one minute and `Now` to `time.Now().UTC`.
3. Read the complete protected set before listing engines.
4. Iterate only engines implementing `viewindex.ManagedIndexLister`.
5. Remove protected or no-longer-present entries from `cleanupCandidates`.
6. Record a newly unreferenced index with its current generation and return without deleting it in the same pass.
7. Collect independent list/delete errors with `errors.Join`.

Use log messages shaped like:

```go
log.Printf("storage view cleanup candidate discovered engine=%s index_id=%s generation=%d first_seen=%s", ref.engine, ref.indexID, generation, now.Format(time.RFC3339Nano))
```

Never log the Metadata auth object or request.

- [ ] **Step 6: Implement due-candidate revalidation and removal**

For each candidate old enough, acquire `s.indexWriteGate(indexID)`, then re-read a complete Metadata/runtime protected set while the gate is held. Under the gate:

- cancel if referenced again;
- cancel if `indexGeneration[indexID] != candidate.generation`;
- call the inventory engine's `Remove`;
- on success remove `indexEngine`, `schemas`, `indexView`, dataset mappings, the candidate, and the matching retirement generation;
- on failure keep the candidate unchanged for the next Timer run.

The helper must return the deletion error instead of starting a goroutine:

```go
func (s *Service) removeRetiredIndex(
	ctx context.Context,
	ref managedIndexRef,
	candidate retiredIndexCandidate,
	metadata MetadataClient,
) error
```

- [ ] **Step 7: Make `PrepareViewIndex` share the cleanup gate**

Move the retirement check inside the per-index gate and before generation advancement:

```go
release, err := s.indexWriteGate(req.GetIndexId()).lock(ctx)
if err != nil {
	return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
}
defer release()
if generation, retiring := s.retiringGeneration(req.GetIndexId()); retiring {
	return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(
		pb.ErrorCode_INNER_ERR,
		fmt.Errorf("view index %q generation %d is pending cleanup", req.GetIndexId(), generation),
	)}, nil
}
s.nextIndexGeneration(req.GetIndexId())
if err := engine.Prepare(ctx, req.GetIndexId(), schema); err != nil {
	return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
}
```

Keep the remainder of runtime attachment outside the engine call exactly as it is today; do not broaden this task into View lifecycle changes.

- [ ] **Step 8: Run the cleanup state-machine tests**

Run:

```bash
cd modules/storage
go test ./internal/service/view -run 'TestCleanupRetiredIndexes' -count=1
go test -race ./internal/service/view -run 'TestCleanupRetiredIndexes' -count=1
```

Expected: PASS. The remove-failure test observes one failed attempt and one later successful retry; the metadata-failure test observes zero calls to `Remove`.

- [ ] **Step 9: Commit the cleanup state machine**

```bash
git add modules/storage/internal/service/view/index_cleanup.go \
  modules/storage/internal/service/view/index_cleanup_test.go \
  modules/storage/internal/service/view/service.go
git commit -m "feat(storage): add retired view index collector"
```

### Task 3: Route Every Successful A/B Switch Through the Collector

**Files:**
- Modify: `modules/storage/internal/service/view/build.go`
- Modify: `modules/storage/internal/service/view/reconcile.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/internal/service/view/service_test.go`

- [ ] **Step 1: Rewrite the existing A/B test as a failing collector integration test**

Rename `TestDuckDBABSwitchKeepsActiveReadableAndDeletesOldFile` to `TestDuckDBABSwitchKeepsActiveReadableUntilCollectorDeletesOldFile` and use official A/B IDs. After `SwitchView`, assert both files exist and queries already read B. Invoke the collector at `T0`, advance to `T0+60s`, invoke it again, then assert A and its WAL are absent while B remains queryable.

The Metadata fake supplied to the collector must expose only B as `ActiveIndexId` and no build.

- [ ] **Step 2: Run the rewritten test and verify the old goroutine behavior fails it**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test ./internal/service/view -run 'TestDuckDBABSwitchKeepsActiveReadableUntilCollectorDeletesOldFile' -count=1
```

Expected: FAIL because the old `scheduleOldIndexRemoval(..., grace=0)` deletes A before the collector's 60-second decision.

- [ ] **Step 3: Remove per-rebuild asynchronous deletion**

In `build.go`:

- change `switchViewLocked` to call `markIndexRetiring(oldID, oldGeneration)` only when `oldID != ""`;
- remove `scheduleOldIndexRemoval` and all `time.NewTimer`/cleanup goroutines;
- retain the `grace time.Duration` parameters for source compatibility but mark them unused with `_ = grace` until a later API cleanup;
- make `SwitchView` return immediately after the in-memory pointer switch;
- make `AttachActiveViewWithGrace` mark a displaced local active index retiring instead of scheduling deletion.

In `reconcile.go`, change the `ReconcilerOptions.Grace` comment to explain that the value is retained for compatibility and physical cleanup is owned by the tRPC cleanup Timer. Do not add startup deletion to `RestoreActiveViews`.

- [ ] **Step 4: Remove the obsolete retrying deletion helper**

Delete `removeIndexAfterGrace` from `service.go`. Keep `removeFailedBuildAtGeneration`: failed in-progress builds are still an immediate build rollback, not retired active-index garbage collection.

- [ ] **Step 5: Add a generation-reuse regression test**

Add a test that marks slot A retiring at generation 1, discovers it, advances the slot generation under its gate to 2, and runs a due cleanup. Assert:

```go
if fake.removeCalls != 0 {
	t.Fatalf("stale generation removed a reused slot")
}
if _, retiring := svc.retiringGeneration(indexID); retiring {
	t.Fatalf("stale retirement marker was not cleared")
}
```

Then call `PrepareViewIndex` for the same slot and require success, proving stale cleanup cannot leave the A/B slot permanently blocked.

- [ ] **Step 6: Run View lifecycle and race tests**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test ./internal/service/view -count=1
CGO_ENABLED=1 go test -race ./internal/service/view -count=1
```

Expected: PASS with no asynchronous cleanup goroutine or data race.

- [ ] **Step 7: Commit the A/B ownership change**

```bash
git add modules/storage/internal/service/view/build.go \
  modules/storage/internal/service/view/reconcile.go \
  modules/storage/internal/service/view/service.go \
  modules/storage/internal/service/view/service_test.go \
  modules/storage/internal/service/view/index_cleanup_test.go
git commit -m "refactor(storage): centralize view index cleanup"
```

### Task 4: Register the Cleanup Through the tRPC Timer Component

**Files:**
- Create: `modules/storage/internal/bootstrap/view_index_cleanup_timer.go`
- Create: `modules/storage/internal/bootstrap/view_index_cleanup_timer_test.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `scripts/tests/contract/test-deploy-moox-storage-view.sh`

- [ ] **Step 1: Add a failing bootstrap registration test**

Test the stable service name and fail-fast behavior. Follow the existing metrics reporter bootstrap test style:

```go
func TestViewIndexCleanupTimerSpec(t *testing.T) {
	if viewIndexCleanupTimerService != "trpc.moox.storage.view.cleanup.timer" {
		t.Fatalf("timer service = %q", viewIndexCleanupTimerService)
	}
	if viewIndexCleanupTimerTimeout != 20*time.Second {
		t.Fatalf("timer timeout = %s", viewIndexCleanupTimerTimeout)
	}
}

func TestRegisterViewIndexCleanupTimerRejectsMissingService(t *testing.T) {
	server := trpc.NewServer()
	err := RegisterViewIndexCleanupTimer(server, func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run the bootstrap test and verify it fails**

Run:

```bash
cd modules/storage
go test ./internal/bootstrap -run 'TestViewIndexCleanupTimer' -count=1
```

Expected: compilation fails because the registration function and constants do not exist.

- [ ] **Step 3: Implement the tRPC Timer registration wrapper**

Create `view_index_cleanup_timer.go`:

```go
package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const viewIndexCleanupTimerService = "trpc.moox.storage.view.cleanup.timer"
const viewIndexCleanupTimerTimeout = 20 * time.Second

func RegisterViewIndexCleanupTimer(s *server.Server, cleanup func(context.Context) error) error {
	if s == nil {
		return fmt.Errorf("storage view index cleanup timer requires a tRPC server")
	}
	service := s.Service(viewIndexCleanupTimerService)
	if service == nil {
		return fmt.Errorf("storage view index cleanup timer service %q is not configured", viewIndexCleanupTimerService)
	}
	job, err := timerjob.New("storage_view_index_cleanup", viewIndexCleanupTimerTimeout, cleanup)
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
```

- [ ] **Step 4: Declare the Timer service in Storage View config**

Add next to the metrics Timer:

```yaml
    - name: trpc.moox.storage.view.cleanup.timer
      port: 20308
      network: "*/30 * * * * *"
      protocol: timer
      timeout: 20000
```

Do not set `startAtOnce`; the first framework tick is discovery-only anyway.

- [ ] **Step 5: Register cleanup before `Serve`**

In `runViewRole`, after the metrics reporter registration and before starting `s.Serve`, register:

```go
cleanupOptions := viewservice.RetiredIndexCleanupOptions{
	Metadata: metadataProxy,
	MinUnreferencedAge: time.Minute,
}
if err := storagebootstrap.RegisterViewIndexCleanupTimer(s, func(ctx context.Context) error {
	return svc.CleanupRetiredIndexes(ctx, cleanupOptions)
}); err != nil {
	return err
}
```

This is the only scheduler. Do not add `time.Ticker`, startup cleanup, or a cleanup call to the reconciler.

- [ ] **Step 6: Add production packaging assertions**

Extend `test-deploy-moox-storage-view.sh` with:

```bash
assert_grep 'name: trpc.moox.storage.view.cleanup.timer' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'port: 20308' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'network: "\*/30 \* \* \* \* \*"' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
assert_grep 'timeout: 20000' "${DEPLOY_DIR}/storage-view/config/trpc_go.yaml"
```

- [ ] **Step 7: Run bootstrap, server, and deployment contract tests**

Run:

```bash
cd modules/storage
go test ./internal/bootstrap ./cmd/server -count=1
cd ../..
bash scripts/test-deploy-moox-storage-view.sh
```

Expected: PASS; the staged `storage-view/config/trpc_go.yaml` contains both the metrics Timer and cleanup Timer.

- [ ] **Step 8: Commit the tRPC Timer wiring**

```bash
git add modules/storage/internal/bootstrap/view_index_cleanup_timer.go \
  modules/storage/internal/bootstrap/view_index_cleanup_timer_test.go \
  modules/storage/cmd/server/main.go \
  modules/storage/config/storage_view/trpc_go.yaml \
  scripts/tests/contract/test-deploy-moox-storage-view.sh
git commit -m "feat(storage): schedule retired view cleanup"
```

### Task 5: Final Regression, Race, and Delivery Verification

**Files:**
- Verify: `modules/storage/internal/service/view/...`
- Verify: `modules/storage/internal/service/viewindex/duckdb/...`
- Verify: `modules/storage/internal/service/viewindex/bleve/...`
- Verify: `modules/storage/internal/bootstrap/...`
- Verify: `modules/storage/cmd/server/...`
- Verify: `scripts/tests/contract/test-deploy-moox-storage-view.sh`

- [ ] **Step 1: Run formatting and static diff checks**

```bash
gofmt -w modules/storage/internal/service/view/index_cleanup.go \
  modules/storage/internal/service/view/index_cleanup_test.go \
  modules/storage/internal/service/view/service.go \
  modules/storage/internal/service/view/build.go \
  modules/storage/internal/service/view/reconcile.go \
  modules/storage/internal/service/viewindex/model.go \
  modules/storage/internal/service/viewindex/duckdb/index_manager.go \
  modules/storage/internal/service/viewindex/duckdb/index_manager_test.go \
  modules/storage/internal/service/viewindex/bleve/index.go \
  modules/storage/internal/service/viewindex/bleve/index_test.go \
  modules/storage/internal/bootstrap/view_index_cleanup_timer.go \
  modules/storage/internal/bootstrap/view_index_cleanup_timer_test.go \
  modules/storage/cmd/server/main.go
git diff --check
```

Expected: no output from `git diff --check`.

- [ ] **Step 2: Run the complete focused Storage test matrix**

```bash
cd modules/storage
CGO_ENABLED=1 go test ./internal/service/view/... \
  ./internal/service/viewindex/duckdb \
  ./internal/service/viewindex/bleve \
  ./internal/bootstrap \
  ./cmd/server -count=1
```

Expected: PASS. A macOS DuckDB linker warning is acceptable only when the test process exits zero.

- [ ] **Step 3: Run the race matrix**

```bash
cd modules/storage
CGO_ENABLED=1 go test -race ./internal/service/view/... \
  ./internal/service/viewindex/duckdb \
  ./internal/service/viewindex/bleve \
  ./internal/bootstrap -count=1
```

Expected: PASS with no race report.

- [ ] **Step 4: Run deployment and workspace contracts**

```bash
bash scripts/test-deploy-moox-storage-view.sh
bash scripts/tests/contract/test-deploy-moox-storage-profile.sh
bash -n scripts/deploy-moox.sh scripts/tests/contract/test-deploy-moox-storage-view.sh
```

Expected: all commands exit zero.

- [ ] **Step 5: Perform an independent code review**

Use a fresh `codeCR` reviewer and require file/line evidence for:

- Metadata fail-closed behavior;
- active/build/runtime protection completeness;
- generation and per-index gate ordering;
- restart rediscovery;
- DuckDB/Bleve root confinement;
- absence of per-rebuild cleanup goroutines;
- tRPC Timer registration and packaging.

Repair every P0-P2 finding and rerun Steps 1-4 before proceeding.

- [ ] **Step 6: Commit final review repairs**

```bash
git status --short
git add modules/storage scripts/tests/contract/test-deploy-moox-storage-view.sh
git commit -m "fix(storage): harden retired index cleanup"
```

If the review required no changes, do not create an empty commit.

- [ ] **Step 7: Record local proof separately from production acceptance**

Record the commit SHA and the exact passing commands. Do not claim production cleanup until a deployed Storage View process has shown:

1. the cleanup Timer service starts without a missing-service error;
2. an A/B switch leaves both slots during the first discovery/grace window;
3. the old DuckDB/Bleve artifact disappears after a later Timer invocation;
4. the active index remains queryable throughout;
5. a Storage View restart can rediscover and remove an orphan created before restart.
