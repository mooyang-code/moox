package rpc

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/stretchr/testify/require"
	trpc "trpc.group/trpc-go/trpc-go"
)

type legacyOwnerStub struct {
	released        []legacyRelease
	claimed         []legacyRelease
	claimGeneration int64
}

type legacyRelease struct {
	spaceID, logicalAccountID, runnerID string
}

func (s *legacyOwnerStub) Validate(context.Context, string, string) error { return nil }
func (s *legacyOwnerStub) Claim(_ context.Context, spaceID, logicalAccountID, runnerID string) error {
	s.claimed = append(s.claimed, legacyRelease{spaceID: spaceID, logicalAccountID: logicalAccountID, runnerID: runnerID})
	return nil
}
func (s *legacyOwnerStub) ClaimWithGeneration(ctx context.Context, spaceID, logicalAccountID, runnerID string) (int64, error) {
	if err := s.Claim(ctx, spaceID, logicalAccountID, runnerID); err != nil {
		return 0, err
	}
	if s.claimGeneration <= 0 {
		return 1, nil
	}
	return s.claimGeneration, nil
}
func (s *legacyOwnerStub) Release(_ context.Context, spaceID, logicalAccountID, runnerID string) error {
	s.released = append(s.released, legacyRelease{spaceID: spaceID, logicalAccountID: logicalAccountID, runnerID: runnerID})
	return nil
}

func TestValidatePoolUDFRequiresRegisteredFunction(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: udf
triggers: {event: {name: ready}}
data: {bar: 1m, calendar: crypto_24x7}
rules: {r: {pool: {udf: missing_udf}, weight: 1}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Service{PoolRegistry: input.NewUDFRegistry()}).validatePoolUDFs(dsl); err == nil {
		t.Fatal("unregistered pool UDF should be rejected before enable")
	}
}

func TestSetRunnerStatusRejectsModernStrategyInstance(t *testing.T) {
	repo := openRPCStore(t)
	definition := store.StrategyDefinition{
		StrategyID: "modern-strategy", StrategyName: "modern", DSLYaml: `name: modern
triggers: {event: {name: source.ready}}
data: {bar: 1m, calendar: crypto_24x7}
rules: {r: {pool: [BTC], score: close, weight: 1}}
`, CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}
	if err := repo.SaveStrategyDefinition(context.Background(), definition); err != nil {
		t.Fatal(err)
	}
	session := "session-modern"
	if err := repo.CreateInstance(context.Background(), store.StrategyInstance{
		InstanceID: "modern-instance", StrategyID: definition.StrategyID, SpaceID: "space",
		InputBindingsJSON: json.RawMessage(`{"source_view_id":"source"}`), Enabled: true, SessionID: &session,
		CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{Repo: repo}
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, "X-Space-Id", []byte("space"))
	response, err := service.SetRunnerStatus(ctx, &strategypb.SetRunnerStatusReq{RunnerId: "modern-instance", Status: string(domain.RunnerStatusDisabled)})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() == 0 {
		t.Fatal("legacy runner RPC unexpectedly mutated a modern instance")
	}
	instance, err := repo.GetInstance(context.Background(), "modern-instance")
	if err != nil {
		t.Fatal(err)
	}
	if !instance.Enabled || instance.SessionID == nil || *instance.SessionID != session {
		t.Fatalf("modern instance lifecycle changed: %+v", instance)
	}
}

func TestStrategyInstanceRPCRequiresMatchingSpaceMetadata(t *testing.T) {
	repo := openRPCStore(t)
	definition := store.StrategyDefinition{StrategyID: "scope-strategy", StrategyName: "scope", DSLYaml: "name: scope\n", CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1)}
	require.NoError(t, repo.SaveStrategyDefinition(context.Background(), definition))
	require.NoError(t, repo.CreateInstance(context.Background(), store.StrategyInstance{
		InstanceID: "scope-instance", StrategyID: definition.StrategyID, SpaceID: "space-a",
		InputBindingsJSON: json.RawMessage(`{}`), CreatedAt: time.UnixMilli(1), UpdatedAt: time.UnixMilli(1),
	}))
	service := &Service{Repo: repo}

	missing, err := service.GetStrategyInstance(trpc.BackgroundContext(), &strategypb.GetStrategyInstanceReq{InstanceId: "scope-instance"})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), missing.GetRetInfo().GetCode())

	mismatch := trpc.BackgroundContext()
	trpc.SetMetaData(mismatch, "X-Space-Id", []byte("space-b"))
	got, err := service.GetStrategyInstance(mismatch, &strategypb.GetStrategyInstanceReq{InstanceId: "scope-instance"})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), got.GetRetInfo().GetCode())

	missingSet, err := service.SetStrategyInstanceEnabled(trpc.BackgroundContext(), &strategypb.SetStrategyInstanceEnabledReq{InstanceId: "scope-instance", Enabled: false})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), missingSet.GetRetInfo().GetCode())
}

type runnerVerifyCatalog struct{}

func (runnerVerifyCatalog) GetFactor(context.Context, string) (compiler.FactorDescriptor, error) {
	return compiler.FactorDescriptor{}, nil
}
func (runnerVerifyCatalog) ListBindings(context.Context, string) ([]compiler.BindingDescriptor, error) {
	return nil, nil
}
func (runnerVerifyCatalog) GetView(_ context.Context, id string) (compiler.ViewDescriptor, error) {
	return compiler.ViewDescriptor{ID: id, Status: "active", Frequency: "1m"}, nil
}
func (runnerVerifyCatalog) ListViewColumns(context.Context, string) ([]compiler.ViewColumn, error) {
	return nil, nil
}

func openRPCStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "strategy.db")
	repo, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	return repo
}
