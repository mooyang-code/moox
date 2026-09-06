package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	trpc "trpc.group/trpc-go/trpc-go"
)

type createSessionOwner struct {
	legacyOwnerStub
	claimErr error
	sessions []string
	onClaim  func()
}

func (s *createSessionOwner) ClaimSession(_ context.Context, _, _, _, session string) error {
	s.sessions = append(s.sessions, session)
	if s.onClaim != nil {
		s.onClaim()
	}
	return s.claimErr
}

func (*createSessionOwner) ReleaseSession(context.Context, string, string, string, string) error {
	return nil
}

type createClaimRejected struct{}

func (createClaimRejected) Error() string { return "owner conflict" }
func (createClaimRejected) Code() int32   { return 14 }

func newCreateInstanceService(t *testing.T) (*Service, context.Context, *strategypb.CreateStrategyInstanceReq, *createSessionOwner) {
	t.Helper()
	repo := openLegacyOwnerStore(t)
	if err := repo.SaveStrategyDefinition(context.Background(), store.StrategyDefinition{
		StrategyID: "create-strategy", StrategyName: "create", DSLYaml: `name: create
triggers: {event: {name: source.ready}}
data: {bar: 1m, calendar: crypto_24x7}
rules: {r: {pool: [BTC], score: close, weight: 1}}
`, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	owner := &createSessionOwner{}
	catalog := runnerVerifyCatalog{}
	svc := &Service{Repo: repo, LogicalAccounts: owner, Compiler: &compiler.Compiler{Factors: catalog, Storage: catalog}}
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, "X-Space-Id", []byte("space"))
	req := &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{
		InstanceId: "create-instance", StrategyId: "create-strategy", SpaceId: "space",
		InputBindingsJson: `{"source_view_id":"source"}`, LogicalAccountId: "logical-a", Enabled: true,
	}}
	return svc, ctx, req, owner
}

func TestCreateInstanceFailureReturnsDurableIdentity(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           error
		retainSession bool
	}{
		{"unknown_claim", errors.New("claim timeout"), true},
		{"rejected_claim", createClaimRejected{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, ctx, req, owner := newCreateInstanceService(t)
			owner.claimErr = tc.err
			rsp, err := svc.CreateStrategyInstance(ctx, req)
			if err != nil || rsp.GetRetInfo().GetCode() == 0 {
				t.Fatalf("expected claim failure: %+v, %v", rsp, err)
			}
			if rsp.GetInstance().GetInstanceId() != req.Instance.InstanceId || rsp.GetInstance().GetEnabled() {
				t.Fatalf("failure lost durable disabled instance: %+v", rsp)
			}
			if (rsp.GetInstance().GetSessionId() != "") != tc.retainSession {
				t.Fatalf("unexpected recovery session: %+v", rsp)
			}
			if !strings.Contains(rsp.GetRetInfo().GetMsg(), req.Instance.InstanceId) {
				t.Fatalf("plain-error clients cannot locate created instance: %+v", rsp)
			}
			owner.claimErr = nil
			restarted := &Service{Repo: svc.Repo, LogicalAccounts: owner, Compiler: svc.Compiler}
			recovered, err := restarted.SetStrategyInstanceEnabled(ctx, &strategypb.SetStrategyInstanceEnabledReq{InstanceId: req.Instance.InstanceId, Enabled: true})
			if err != nil || recovered.GetRetInfo().GetCode() != 0 || !recovered.GetInstance().GetEnabled() || recovered.GetInstance().GetSessionId() == "" {
				t.Fatalf("explicit enable after restart failed: %+v, %v", recovered, err)
			}
			if (owner.sessions[0] == owner.sessions[1]) != tc.retainSession {
				t.Fatalf("recovery did not respect retained/rejected session: %v", owner.sessions)
			}
		})
	}
}

func TestCreateInstanceEnableWriteFailureRetainsClaimEvidence(t *testing.T) {
	svc, ctx, req, owner := newCreateInstanceService(t)
	if err := svc.Repo.ApplySchema(`CREATE TRIGGER reject_create_enable BEFORE UPDATE OF enabled ON t_strategy_instances
WHEN NEW.enabled = 1 BEGIN SELECT RAISE(ABORT, 'enable write failed'); END;`); err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.CreateStrategyInstance(ctx, req)
	if err != nil || rsp.GetRetInfo().GetCode() == 0 || rsp.GetInstance().GetInstanceId() != req.Instance.InstanceId {
		t.Fatalf("enable write failure lost durable identity: %+v, %v", rsp, err)
	}
	if rsp.GetInstance().GetEnabled() || len(owner.sessions) != 1 || rsp.GetInstance().GetSessionId() != owner.sessions[0] {
		t.Fatalf("enable write failure lost claim evidence: %+v; claims=%v", rsp, owner.sessions)
	}
	if err := svc.Repo.ApplySchema(`DROP TRIGGER reject_create_enable;`); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{Repo: svc.Repo, LogicalAccounts: owner, Compiler: svc.Compiler}
	recovered, err := restarted.SetStrategyInstanceEnabled(ctx, &strategypb.SetStrategyInstanceEnabledReq{InstanceId: req.Instance.InstanceId, Enabled: true})
	if err != nil || recovered.GetRetInfo().GetCode() != 0 || recovered.GetInstance().GetSessionId() != rsp.GetInstance().GetSessionId() || !recovered.GetInstance().GetEnabled() {
		t.Fatalf("write failure did not recover original claimed session: %+v, %v", recovered, err)
	}
}

func TestCreateInstanceRetryNeverChangesEnabledLifecycle(t *testing.T) {
	svc, ctx, req, owner := newCreateInstanceService(t)
	first, err := svc.CreateStrategyInstance(ctx, req)
	if err != nil || first.GetRetInfo().GetCode() != 0 {
		t.Fatalf("create: %+v, %v", first, err)
	}
	for _, desired := range []bool{true, false} {
		req.Instance.Enabled = desired
		retry, err := svc.CreateStrategyInstance(ctx, req)
		if err != nil || (retry.GetRetInfo().GetCode() == 0) != desired || !retry.GetInstance().GetEnabled() || retry.GetInstance().GetSessionId() != first.GetInstance().GetSessionId() || len(owner.sessions) != 1 {
			t.Fatalf("retry changed enabled lifecycle: %+v, %v claims=%v", retry, err, owner.sessions)
		}
	}
}

func TestEqualInstanceBindingsPreservesStructuredIdentity(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		equal       bool
	}{
		{`{"b":[1,2],"a":{"x":true}}`, ` { "a": { "x": true }, "b": [1, 2] } `, true},
		{"", `{}`, true},
		{`{"n":9007199254740992}`, `{"n":9007199254740993}`, false},
		{`{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{`{}`, `{} {}`, false},
	} {
		if got := equalInstanceBindings([]byte(tc.left), []byte(tc.right)); got != tc.equal {
			t.Fatalf("equalInstanceBindings(%s, %s) = %t", tc.left, tc.right, got)
		}
	}
}

func TestCreateInstanceRetryReturnsExistingStateWithoutClaim(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "pending_enable"}[enabled], func(t *testing.T) {
			svc, ctx, req, owner := newCreateInstanceService(t)
			req.Instance.Enabled = enabled
			owner.claimErr = errors.New("unknown claim")
			first, _ := svc.CreateStrategyInstance(ctx, req)
			before, err := svc.Repo.GetInstance(ctx, req.Instance.InstanceId)
			if err != nil {
				t.Fatal(err)
			}
			req.Instance.InputBindingsJson = ` { "source_view_id" : "source" } `
			owner.claimErr = nil
			// A fresh service proves this is durable behavior, not an in-memory request cache.
			restarted := &Service{Repo: svc.Repo, LogicalAccounts: owner, Compiler: svc.Compiler}
			retry, err := restarted.CreateStrategyInstance(ctx, req)
			if err != nil || retry.GetInstance().GetInstanceId() != first.GetInstance().GetInstanceId() || retry.GetInstance().GetInstanceId() == "" {
				t.Fatalf("retry lost durable identity: %+v, %v", retry, err)
			}
			if (retry.GetRetInfo().GetCode() != 0) != enabled {
				t.Fatalf("retry must report current state versus desired enable: %+v", retry)
			}
			if enabled && !strings.Contains(retry.GetRetInfo().GetMsg(), "SetStrategyInstanceEnabled") {
				t.Fatalf("retry does not identify explicit recovery RPC: %+v", retry)
			}
			after, err := svc.Repo.GetInstance(ctx, req.Instance.InstanceId)
			if err != nil || !reflect.DeepEqual(before, after) || len(owner.sessions) != map[bool]int{false: 0, true: 1}[enabled] {
				t.Fatalf("Create retry changed state or claimed: before=%+v after=%+v sessions=%v err=%v", before, after, owner.sessions, err)
			}
			if enabled {
				rsp, err := restarted.SetStrategyInstanceEnabled(ctx, &strategypb.SetStrategyInstanceEnabledReq{InstanceId: req.Instance.InstanceId, Enabled: true})
				if err != nil || rsp.GetRetInfo().GetCode() != 0 || !rsp.GetInstance().GetEnabled() || rsp.GetInstance().GetSessionId() != first.GetInstance().GetSessionId() {
					t.Fatalf("explicit enable failed to recover original session: %+v, %v", rsp, err)
				}
			}
		})
	}
}

func TestCreateInstanceRetryRejectsDifferentConfiguration(t *testing.T) {
	for _, field := range []string{"space", "strategy", "bindings", "account"} {
		t.Run(field, func(t *testing.T) {
			svc, ctx, req, owner := newCreateInstanceService(t)
			req.Instance.Enabled = false
			first, _ := svc.CreateStrategyInstance(ctx, req)
			if first.GetRetInfo().GetCode() != 0 {
				t.Fatalf("create: %+v", first)
			}
			before, _ := svc.Repo.GetInstance(ctx, req.Instance.InstanceId)
			switch field {
			case "space":
				req.Instance.SpaceId = "other"
				trpc.SetMetaData(ctx, "X-Space-Id", []byte("other"))
			case "strategy":
				req.Instance.StrategyId = "other"
			case "bindings":
				req.Instance.InputBindingsJson = `{"source_view_id":"other"}`
			case "account":
				req.Instance.LogicalAccountId = "other"
			}
			rsp, err := svc.CreateStrategyInstance(ctx, req)
			if err != nil || rsp.GetRetInfo().GetCode() == 0 || !strings.Contains(rsp.GetRetInfo().GetMsg(), "conflict") || rsp.GetInstance() != nil {
				t.Fatalf("different config was not an isolated conflict: %+v, %v", rsp, err)
			}
			after, _ := svc.Repo.GetInstance(ctx, before.InstanceID)
			if !reflect.DeepEqual(before, after) || len(owner.sessions) != 0 {
				t.Fatal("conflict mutated existing instance")
			}
		})
	}
}

func TestCreateInstanceReadFailureReturnsOnlyIdentity(t *testing.T) {
	svc, ctx, req, _ := newCreateInstanceService(t)
	if err := svc.Repo.ApplySchema(`CREATE TRIGGER break_create_read AFTER UPDATE OF enabled ON t_strategy_instances
WHEN NEW.enabled = 1 BEGIN UPDATE t_strategy_instances SET created_at = 'unreadable' WHERE instance_id = NEW.instance_id; END;`); err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.CreateStrategyInstance(ctx, req)
	if err != nil || rsp.GetRetInfo().GetCode() == 0 || rsp.GetInstance().GetInstanceId() != req.Instance.InstanceId || rsp.GetInstance().GetEnabled() || rsp.GetInstance().GetSessionId() != "" || rsp.GetInstance().GetCreatedAt() != "" {
		t.Fatalf("read failure must return identity without claiming a known state: %+v, %v", rsp, err)
	}
	if !strings.Contains(rsp.GetRetInfo().GetMsg(), "state unavailable") {
		t.Fatalf("successful enable misreported: %+v", rsp)
	}
}

func TestCreateInstanceEnableAndReadFailureReturnsOnlyIdentity(t *testing.T) {
	svc, ctx, req, owner := newCreateInstanceService(t)
	owner.onClaim = func() {
		if err := svc.Repo.Close(); err != nil {
			t.Fatal(err)
		}
	}
	rsp, err := svc.CreateStrategyInstance(ctx, req)
	if err != nil || rsp.GetRetInfo().GetCode() == 0 {
		t.Fatalf("expected persistence failure: %+v, %v", rsp, err)
	}
	want := &strategypb.StrategyInstance{InstanceId: req.Instance.InstanceId, StrategyId: req.Instance.StrategyId, SpaceId: req.Instance.SpaceId}
	if !reflect.DeepEqual(rsp.GetInstance(), want) {
		t.Fatalf("unreadable durable instance exposes guessed lifecycle: %+v", rsp)
	}
	msg := rsp.GetRetInfo().GetMsg()
	if !strings.Contains(msg, "state unavailable") || !strings.Contains(msg, "read created instance") || strings.Count(msg, "database is closed") < 2 {
		t.Fatalf("write/read error chain lost: %s", msg)
	}
}

func TestCreateInstanceClaimCleanupFailureIsVisible(t *testing.T) {
	svc, ctx, req, owner := newCreateInstanceService(t)
	owner.claimErr = createClaimRejected{}
	if err := svc.Repo.ApplySchema(`CREATE TRIGGER reject_create_clear BEFORE UPDATE OF session_id ON t_strategy_instances
WHEN NEW.session_id IS NULL BEGIN SELECT RAISE(ABORT, 'cleanup unavailable'); END;`); err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.CreateStrategyInstance(ctx, req)
	if err != nil || rsp.GetRetInfo().GetCode() == 0 || !strings.Contains(rsp.GetRetInfo().GetMsg(), "cleanup unavailable") || rsp.GetInstance().GetSessionId() == "" {
		t.Fatalf("cleanup failure lost error or recovery evidence: %+v, %v", rsp, err)
	}
}

func TestCreateInstanceClaimRejectsConcurrentLegacyUpdateRunner(t *testing.T) {
	svc, ctx, req, owner := newCreateInstanceService(t)
	compiled, err := json.Marshal(compiler.CompiledStrategy{
		APIVersion: config.APIVersion, Kind: config.Kind, SpaceID: "space",
		SourceView: compiler.CompiledView{ID: "other-source", Status: "active", Frequency: "1m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo.SaveStrategy(ctx, domain.Strategy{ID: "legacy-replacement", Name: "replacement", Kind: config.Kind, CompiledJSON: compiled, CreatedAt: time.UnixMilli(1)}); err != nil {
		t.Fatal(err)
	}
	var update *strategypb.UpdateRunnerRsp
	owner.onClaim = func() {
		var err error
		update, err = svc.UpdateRunner(ctx, &strategypb.UpdateRunnerReq{Runner: &strategypb.StrategyRunner{
			RunnerId: req.Instance.InstanceId, StrategyId: "legacy-replacement", SpaceId: "space", LogicalAccountId: "logical-b",
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	rsp, err := svc.CreateStrategyInstance(ctx, req)
	if err != nil || rsp.GetRetInfo().GetCode() != 0 {
		t.Fatalf("create failed: %+v, %v", rsp, err)
	}
	if update == nil || update.GetRetInfo().GetCode() == 0 {
		t.Fatalf("legacy update mutated modern pending claim: %+v", update)
	}
	if rsp.GetInstance().GetStrategyId() != req.Instance.StrategyId || rsp.GetInstance().GetLogicalAccountId() != req.Instance.LogicalAccountId || rsp.GetInstance().GetSessionId() != owner.sessions[0] {
		t.Fatalf("modern claim enabled a changed identity: %+v", rsp)
	}
	if len(owner.released) != 0 {
		t.Fatalf("rejected legacy update released modern ownership: %+v", owner.released)
	}
}
