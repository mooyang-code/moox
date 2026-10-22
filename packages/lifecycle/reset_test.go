package lifecycle

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/stretchr/testify/require"
)

// resetFixture 是带重置实现的控制器：两步，第二步可以被测试注入失败。
type resetFixture struct {
	c        *Controller
	dir      string
	runs     map[string]*atomic.Int32
	failStep atomic.Value // string：这一步返回错误
	blockers atomic.Value // []string
	now      time.Time
	audit    []string // 审计事件
	auditMu  sync.Mutex
}

func newResetFixture(t *testing.T) *resetFixture {
	t.Helper()
	f := &resetFixture{dir: t.TempDir(), runs: map[string]*atomic.Int32{"backup": {}, "clear": {}, "seed": {}}, now: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)}
	f.failStep.Store("")
	f.blockers.Store([]string(nil))
	step := func(name string) ResetStep {
		return ResetStep{Name: name, Describe: "步骤 " + name, Run: func(context.Context, string) error {
			f.runs[name].Add(1)
			if f.failStep.Load().(string) == name {
				return errors.New("磁盘写满")
			}
			return nil
		}}
	}
	c, err := New(Options{
		Module: "demo", StateFile: filepath.Join(f.dir, "lifecycle.json"), Now: func() time.Time { return f.now },
		Audit: func(e string) { f.auditMu.Lock(); f.audit = append(f.audit, e); f.auditMu.Unlock() },
		Hooks: Hooks{
			Objects:     func() []Ref { return []Ref{set1} },
			CheckObject: func(context.Context, Ref) []Check { return []Check{{Name: "x", Status: CheckPass}} },
			Reset: &ResetSpec{Plan: func(_ context.Context, scope ResetScope) (ResetPlanInfo, []ResetStep, error) {
				steps := []ResetStep{step("backup"), step("clear")}
				if scope == ResetAll {
					steps = append(steps, step("seed"))
				}
				return ResetPlanInfo{Keeps: []string{"身份"}, Notes: []string{"不备份：大文件"}, Stats: map[string]any{"rows": 123}, Blockers: f.blockers.Load().([]string)}, steps, nil
			}},
		},
	})
	require.NoError(t, err)
	f.c = c
	c.Refresh(context.Background())
	return f
}

func (f *resetFixture) pause(t *testing.T) {
	t.Helper()
	_, err := f.c.Execute(context.Background(), Request{Command: CmdPause, PauseKind: PauseSystem})
	require.NoError(t, err)
}

func (f *resetFixture) applyNew(t *testing.T, scope ResetScope) ResetStatus {
	t.Helper()
	plan, err := f.c.ResetPlan(context.Background(), scope)
	require.NoError(t, err)
	st, err := f.c.ResetApply(context.Background(), ResetApplyRequest{Token: plan.Token, Scope: scope, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, f.c.ResetWait(context.Background()))
	return st
}

func TestResetPlanIsReadOnlyAndStable(t *testing.T) {
	f := newResetFixture(t)
	ctx := context.Background()
	a, err := f.c.ResetPlan(ctx, ResetAll)
	require.NoError(t, err)
	require.Len(t, a.Steps, 3)
	require.Equal(t, []string{"身份"}, a.Keeps)
	require.Equal(t, 123, a.Stats["rows"])
	require.NotEmpty(t, a.Token)
	require.Equal(t, f.now.Add(ResetTokenTTL), a.ExpiresAt)
	require.Zero(t, f.runs["backup"].Load(), "plan 不执行任何步骤")

	b, err := f.c.ResetPlan(ctx, ResetAll)
	require.NoError(t, err)
	require.Equal(t, a.Digest, b.Digest, "摘要只含稳定内容，统计值变化不影响")
	d, err := f.c.ResetPlan(ctx, ResetData)
	require.NoError(t, err)
	require.NotEqual(t, a.Digest, d.Digest)
	require.Len(t, d.Steps, 2)
	_, err = f.c.ResetPlan(ctx, "bogus")
	require.Error(t, err)

	f.blockers.Store([]string{"账户 A 还有持仓"})
	blocked, err := f.c.ResetPlan(ctx, ResetAll)
	require.NoError(t, err)
	require.Equal(t, []string{"账户 A 还有持仓"}, blocked.Blockers)
	require.Empty(t, blocked.Token, "有阻止项就不发令牌")
}

func TestResetApplyRequiresPausedModuleAndValidSingleUseToken(t *testing.T) {
	f := newResetFixture(t)
	ctx := context.Background()
	plan, err := f.c.ResetPlan(ctx, ResetAll)
	require.NoError(t, err)

	var denied *ErrReset
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{Token: plan.Token, Scope: ResetAll})
	require.ErrorAs(t, err, &denied, "模块没暂停不能重置")
	require.Contains(t, err.Error(), "已暂停")
	require.Zero(t, f.runs["backup"].Load())

	f.pause(t)
	// 模块没暂停时的拒绝发生在令牌被消耗之前：暂停之后同一个令牌仍然有效。
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{Token: "bogus", Scope: ResetAll})
	require.ErrorAs(t, err, &denied)
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{Scope: ResetAll})
	require.ErrorAs(t, err, &denied, "没有令牌")
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{Token: plan.Token, Scope: ResetData})
	require.ErrorAs(t, err, &denied, "范围和令牌不符")

	// 范围和令牌不符的请求会消耗掉这个令牌（防止猜测），重新 plan 拿新的。
	plan2, err := f.c.ResetPlan(ctx, ResetAll)
	require.NoError(t, err)
	st, err := f.c.ResetApply(ctx, ResetApplyRequest{Token: plan2.Token, Scope: ResetAll, By: "ops"})
	require.NoError(t, err)
	require.Equal(t, StageResetting, st.Stage)
	require.NoError(t, f.c.ResetWait(ctx))
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{Token: plan2.Token, Scope: ResetAll})
	require.ErrorAs(t, err, &denied, "令牌只能用一次")
}

func TestResetTokenExpiresAndPlanChangeInvalidatesIt(t *testing.T) {
	f := newResetFixture(t)
	ctx := context.Background()
	f.pause(t)
	plan, err := f.c.ResetPlan(ctx, ResetAll)
	require.NoError(t, err)
	f.now = f.now.Add(ResetTokenTTL + time.Second)
	var denied *ErrReset
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{Token: plan.Token, Scope: ResetAll})
	require.ErrorAs(t, err, &denied)
	require.Contains(t, err.Error(), "过期")

	// 令牌之后计划变了（步骤不同）：令牌作废。
	plan, err = f.c.ResetPlan(ctx, ResetAll)
	require.NoError(t, err)
	f.c.opts.Hooks.Reset.Plan = func(context.Context, ResetScope) (ResetPlanInfo, []ResetStep, error) {
		return ResetPlanInfo{}, []ResetStep{{Name: "other", Describe: "别的", Run: func(context.Context, string) error { return nil }}}, nil
	}
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{Token: plan.Token, Scope: ResetAll})
	require.ErrorAs(t, err, &denied)
	require.Contains(t, err.Error(), "计划已经变化")
}

// 成功：步骤按顺序执行并写日志；模块回到未初始化、保持「重置后等待初始化」，对象清空，记录最近一次重置；
// 自动初始化不会把它放出来，用户 init 才解除。
func TestResetSuccessLeavesModuleUninitializedUntilInit(t *testing.T) {
	f := newResetFixture(t)
	ctx := context.Background()
	f.pause(t)
	st := f.applyNew(t, ResetAll)
	require.Equal(t, StageResetting, st.Stage)
	for _, name := range []string{"backup", "clear", "seed"} {
		require.Equal(t, int32(1), f.runs[name].Load(), name)
	}
	final := f.c.ResetStatus()
	require.Equal(t, StageUninitialized, final.Stage)
	require.Equal(t, "done", final.Operation.State)
	require.Equal(t, "ops", final.LastReset.By)
	require.Equal(t, ResetAll, final.LastReset.Scope)
	for _, s := range final.Operation.Steps {
		require.Equal(t, "done", s.State)
	}
	snap := f.c.Snapshot()
	require.Equal(t, HoldPostReset, snap.Self.Hold)
	require.Equal(t, StageUninitialized, snap.Self.Stage)

	f.c.Refresh(ctx)
	require.Equal(t, StageUninitialized, f.c.Stage(Ref{}), "Refresh 不自动初始化模块自身")
	require.False(t, f.c.Allowed(set1))
	require.False(t, f.c.CatchupAllowed(set1))
	var afterResetCalls atomic.Int32
	f.c.opts.Hooks.AfterReset = func(context.Context) { afterResetCalls.Add(1) }
	obj, err := f.c.Command(ctx, CmdInit, Ref{})
	require.NoError(t, err)
	require.Equal(t, StageReady, obj.Stage)
	require.Equal(t, HoldNone, obj.Hold)
	require.Equal(t, int32(1), afterResetCalls.Load(), "重置后第一次 init 触发 AfterReset")
	_, err = f.c.Command(ctx, CmdInit, Ref{})
	require.NoError(t, err)
	require.Equal(t, int32(1), afterResetCalls.Load(), "之后的 init 不再触发")
	require.NotNil(t, obj.LastReset)
}

// 失败：停在 reset_failed，其余命令一律拒绝；凭 operation_id 续做时跳过已完成的步骤，不重新来过。
func TestResetFailureResumesFromTheFailedStepWithoutRedoingDoneSteps(t *testing.T) {
	f := newResetFixture(t)
	ctx := context.Background()
	f.pause(t)
	f.failStep.Store("clear")
	f.applyNew(t, ResetAll)
	st := f.c.ResetStatus()
	require.Equal(t, StageResetFailed, st.Stage)
	require.Equal(t, "failed", st.Operation.State)
	require.Contains(t, st.Operation.Error, "第 2 步 clear 失败")
	require.Equal(t, "done", st.Operation.Steps[0].State)
	require.Equal(t, "failed", st.Operation.Steps[1].State)
	require.Equal(t, "pending", st.Operation.Steps[2].State)

	var notAllowed *ErrNotAllowed
	_, err := f.c.Command(ctx, CmdResume, Ref{})
	require.ErrorAs(t, err, &notAllowed, "重置失败后不能 resume")
	_, err = f.c.Command(ctx, CmdStart, set1)
	require.ErrorAs(t, err, &notAllowed)
	require.False(t, f.c.Allowed(set1))

	// 新发起被拒绝：上一次没有完成。
	plan, err := f.c.ResetPlan(ctx, ResetAll)
	require.NoError(t, err)
	var denied *ErrReset
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{Token: plan.Token, Scope: ResetAll})
	require.ErrorAs(t, err, &denied)
	require.Contains(t, err.Error(), "没有完成")

	f.failStep.Store("")
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{OperationID: st.Operation.OperationID})
	require.NoError(t, err)
	require.NoError(t, f.c.ResetWait(ctx))
	require.Equal(t, int32(1), f.runs["backup"].Load(), "已完成的备份不重做")
	require.Equal(t, int32(2), f.runs["clear"].Load(), "失败的那步重试")
	require.Equal(t, int32(1), f.runs["seed"].Load())
	require.Equal(t, StageUninitialized, f.c.ResetStatus().Stage)
	require.Equal(t, "done", f.c.ResetStatus().Operation.State)
}

// 续做不依赖令牌（令牌 10 分钟过期不影响续做），且进程重启后日志仍在。
func TestResetResumeSurvivesRestartAndTokenExpiry(t *testing.T) {
	f := newResetFixture(t)
	ctx := context.Background()
	f.pause(t)
	f.failStep.Store("clear")
	f.applyNew(t, ResetAll)
	op := f.c.ResetStatus().Operation.OperationID
	f.now = f.now.Add(time.Hour) // 令牌早已过期

	// 模拟重启：用同一个状态文件重新创建控制器。
	spec := f.c.opts.Hooks.Reset
	c2, err := New(Options{Module: "demo", StateFile: filepath.Join(f.dir, "lifecycle.json"), Now: func() time.Time { return f.now }, Hooks: Hooks{Reset: spec,
		Objects: func() []Ref { return nil }}})
	require.NoError(t, err)
	st := c2.ResetStatus()
	require.Equal(t, StageResetFailed, st.Stage)
	require.Equal(t, op, st.Operation.OperationID)
	f.failStep.Store("")
	_, err = c2.ResetApply(ctx, ResetApplyRequest{OperationID: op})
	require.NoError(t, err)
	require.NoError(t, c2.ResetWait(ctx))
	require.Equal(t, StageUninitialized, c2.ResetStatus().Stage)
	require.Equal(t, "done", c2.ResetStatus().Operation.State)
}

// 进程在重置中途退出（日志里仍是 running、阶段是 resetting）：重启后停在 reset_failed，可续做。
func TestResetInterruptedByRestartBecomesFailed(t *testing.T) {
	f := newResetFixture(t)
	f.pause(t)
	plan, err := f.c.ResetPlan(context.Background(), ResetAll)
	require.NoError(t, err)
	block := make(chan struct{})
	f.c.opts.Hooks.Reset.Plan = func(context.Context, ResetScope) (ResetPlanInfo, []ResetStep, error) {
		return ResetPlanInfo{}, []ResetStep{{Name: "slow", Describe: "慢", Run: func(ctx context.Context, _ string) error { <-block; return nil }}}, nil
	}
	plan, err = f.c.ResetPlan(context.Background(), ResetAll)
	require.NoError(t, err)
	_, err = f.c.ResetApply(context.Background(), ResetApplyRequest{Token: plan.Token, Scope: ResetAll})
	require.NoError(t, err)
	require.Equal(t, StageResetting, f.c.ResetStatus().Stage)

	c2, err := New(Options{Module: "demo", StateFile: filepath.Join(f.dir, "lifecycle.json"), Now: func() time.Time { return f.now }, Hooks: Hooks{Objects: func() []Ref { return nil }}})
	require.NoError(t, err)
	st := c2.ResetStatus()
	require.Equal(t, StageResetFailed, st.Stage)
	require.Equal(t, "failed", st.Operation.State)
	require.Contains(t, st.Operation.Error, "中断")
	close(block)
	require.NoError(t, f.c.ResetWait(context.Background()))
}

func TestResetAbortKeepsTheModuleBlocked(t *testing.T) {
	f := newResetFixture(t)
	ctx := context.Background()
	var denied *ErrReset
	_, err := f.c.ResetAbort(ctx, "ops")
	require.ErrorAs(t, err, &denied, "没有失败的重置")

	f.pause(t)
	f.failStep.Store("clear")
	f.applyNew(t, ResetAll)
	st, err := f.c.ResetAbort(ctx, "ops")
	require.NoError(t, err)
	require.Equal(t, StageUninitialized, st.Stage)
	require.Equal(t, "aborted", st.Operation.State)
	require.Equal(t, HoldPostReset, f.c.Snapshot().Self.Hold, "放弃不会放行没清理完的对象")
	f.c.Refresh(ctx)
	require.Equal(t, StageUninitialized, f.c.Stage(Ref{}))
	_, err = f.c.ResetApply(ctx, ResetApplyRequest{OperationID: st.Operation.OperationID})
	require.ErrorAs(t, err, &denied, "已放弃的操作不能续做")
}

// HTTP：plan / apply / abort 要 reset 权限，status 不要；权限来自服务端设置的请求上下文，请求体里自称没用。
func TestResetHTTPRequiresServerGrantedPermission(t *testing.T) {
	f := newResetFixture(t)
	f.pause(t)
	handler := f.c.ResetHandler()
	do := func(method, action string, body any, withPermission bool) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, ResetPrefix+action, bytes.NewReader(raw))
		if withPermission {
			req = req.WithContext(requestauth.WithPermissions(req.Context(), requestauth.PermissionReset))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, _ := do(http.MethodPost, "plan", map[string]any{"scope": "all"}, false)
	require.Equal(t, http.StatusForbidden, code, "运行时密钥不能 plan")
	code, _ = do(http.MethodPost, "apply", map[string]any{"token": "x", "scope": "all"}, false)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(http.MethodPost, "abort", map[string]any{}, false)
	require.Equal(t, http.StatusForbidden, code)
	code, _ = do(http.MethodGet, "status", nil, false)
	require.Equal(t, http.StatusOK, code, "status 只读，运行时密钥也能看")
	code, _ = do(http.MethodPost, "status", nil, true)
	require.Equal(t, http.StatusMethodNotAllowed, code)

	code, plan := do(http.MethodPost, "plan", map[string]any{"scope": "all"}, true)
	require.Equal(t, http.StatusOK, code)
	token, _ := plan["token"].(string)
	require.NotEmpty(t, token)
	code, _ = do(http.MethodPost, "apply", map[string]any{"token": token, "scope": "all", "by": "ops"}, true)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, f.c.ResetWait(context.Background()))
	code, out := do(http.MethodPost, "apply", map[string]any{"token": token, "scope": "all"}, true)
	require.Equal(t, http.StatusConflict, code, "令牌已用过：被拒绝是 409")
	require.Contains(t, out["error"], "令牌")
	code, _ = do(http.MethodPost, "apply", map[string]any{"token": "x", "extra": 1}, true)
	require.Equal(t, http.StatusBadRequest, code, "未知字段")
	code, _ = do(http.MethodGet, "nope", nil, true)
	require.Equal(t, http.StatusMethodNotAllowed, code)
}

// 不支持重置的模块：所有重置接口报错。
func TestModuleWithoutResetSpecRejects(t *testing.T) {
	c, err := New(Options{Module: "plain"})
	require.NoError(t, err)
	_, err = c.ResetPlan(context.Background(), ResetAll)
	require.Error(t, err)
	_, err = c.ResetApply(context.Background(), ResetApplyRequest{})
	require.Error(t, err)
}

// SQLite 工具：备份是可用的库文件；清表保留结构、重置自增、推迟外键检查；备份目录只保留最近几份。
func TestSQLiteResetHelpers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "demo.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA foreign_keys = ON;
CREATE TABLE t_parent (c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_name TEXT);
CREATE TABLE t_child (c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_parent INTEGER NOT NULL REFERENCES t_parent (c_id));
CREATE TABLE schema_meta (c_key TEXT);
INSERT INTO t_parent (c_name) VALUES ('a'), ('b');
INSERT INTO t_child (c_parent) VALUES (1), (2);`)
	require.NoError(t, err)

	tables, err := BusinessTables(ctx, db)
	require.NoError(t, err)
	require.Equal(t, []string{"t_child", "t_parent"}, tables, "只列 t_ 开头的业务表")
	unclassified, missing := ClassifyTables(tables, []string{"t_parent"}, []string{"t_gone"})
	require.Equal(t, []string{"t_child"}, unclassified)
	require.Equal(t, []string{"t_gone"}, missing)
	rows, err := TableRows(ctx, db, tables)
	require.NoError(t, err)
	require.Equal(t, int64(2), rows["t_parent"])
	_, err = TableRows(ctx, db, []string{"t_parent; DROP TABLE t_child"})
	require.Error(t, err, "表名只允许标识符")

	backupDir, err := ResetBackupDir(dir, "reset-20261022T090000-aa", 2)
	require.NoError(t, err)
	path, err := SQLiteBackup(ctx, db, backupDir, "demo.db")
	require.NoError(t, err)
	path, err = SQLiteBackup(ctx, db, backupDir, "demo.db") // 续做时重复备份
	require.NoError(t, err)
	backup, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer backup.Close()
	var n int
	require.NoError(t, backup.QueryRow("SELECT COUNT(*) FROM t_parent").Scan(&n))
	require.Equal(t, 2, n, "备份包含当时的全部数据")

	deleted, err := ClearTables(ctx, db, []string{"t_parent", "t_child"}) // 父表先于子表：外键检查被推迟
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"t_parent": 2, "t_child": 2}, deleted)
	_, err = db.Exec("INSERT INTO t_parent (c_name) VALUES ('c')")
	require.NoError(t, err)
	var id int
	require.NoError(t, db.QueryRow("SELECT c_id FROM t_parent").Scan(&id))
	require.Equal(t, 1, id, "自增序列被重置")
	_, err = ClearTables(ctx, db, []string{"t_parent; --"})
	require.Error(t, err)

	for _, id := range []string{"reset-20261022T090100-bb", "reset-20261022T090200-cc"} {
		_, err = ResetBackupDir(dir, id, 2)
		require.NoError(t, err)
	}
	_, statErr := ResetBackupDir(dir, "reset-20261022T090300-dd", 2)
	require.NoError(t, statErr)
	entries, _ := filepath.Glob(filepath.Join(dir, "reset-backups", "*"))
	require.Len(t, entries, 2, "只保留最近 2 份")
}

// 审计事件：暂停、重置开始、每一步、结束、失败、放弃都带操作者和范围。
func TestResetAuditEvents(t *testing.T) {
	f := newResetFixture(t)
	f.pause(t)
	f.failStep.Store("clear")
	plan, err := f.c.ResetPlan(context.Background(), ResetData)
	require.NoError(t, err)
	_, err = f.c.ResetApply(context.Background(), ResetApplyRequest{Token: plan.Token, Scope: ResetData, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, f.c.ResetWait(context.Background()))
	_, err = f.c.ResetAbort(context.Background(), "ops")
	require.NoError(t, err)

	f.auditMu.Lock()
	defer f.auditMu.Unlock()
	all := strings.Join(f.audit, "\n")
	for _, want := range []string{"lifecycle_pause", "lifecycle_reset_start", "scope=data", "by=ops", "lifecycle_reset_step", "step=backup", "lifecycle_reset_failed", "step=clear", "lifecycle_reset_abort"} {
		require.Contains(t, all, want)
	}
}
