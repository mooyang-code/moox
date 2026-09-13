package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestEngineRuntimeCloseOrderAndFailureCleanup(t *testing.T) {
	var calls []string
	failure := errors.New("subject close failed")
	r := &EngineRuntime{Health: health.New("factor-engine", "test", "", ""),
		cancel:         func() { calls = append(calls, "cancel") },
		stopSubject:    func() error { calls = append(calls, "subject"); return failure },
		stopCatalog:    func() error { calls = append(calls, "catalog"); return nil },
		closeResources: func() error { calls = append(calls, "resources"); return nil },
	}
	r.Health.SetReady(true)
	for range 2 {
		if !errors.Is(r.Close(), failure) {
			t.Fatal("lost close error")
		}
	}
	if !reflect.DeepEqual(calls, []string{"cancel", "subject", "catalog", "resources"}) {
		t.Fatalf("close order: %v", calls)
	}
	if r.Health.Ready() {
		t.Fatal("closed runtime ready")
	}
}

func TestEngineRuntimeRejectsUnsupportedConfiguration(t *testing.T) {
	if err := validateEngineRuntime(nil, nil); err == nil {
		t.Fatal("accepted nil config")
	}
	cfg := DefaultEngineApplicationConfig()
	cfg.Cache.Enabled = true
	if err := validateEngineRuntime(&server.Server{}, cfg); err == nil {
		t.Fatal("accepted enabled cache")
	}
	cfg.Cache.Enabled = false
	if err := validateEngineRuntime(&server.Server{}, cfg); err == nil {
		t.Fatal("accepted missing services")
	}
}

func TestSubjectOnlyActivationRejectsCrossBeforeCommit(t *testing.T) {
	called := false
	activate := subjectOnlyActivation(func(_ context.Context, _, _ domain.CatalogSnapshot, commit func() error) error {
		called = true
		return commit()
	})
	for _, kind := range []string{domain.FactorTypeCrossSection, ""} {
		err := activate(context.Background(), domain.CatalogSnapshot{}, domain.CatalogSnapshot{Factors: []domain.FactorDef{{FactorID: "test", FactorType: kind}}}, func() error { t.Fatal("committed unsupported catalog"); return nil })
		if err == nil || called {
			t.Fatal("accepted unsupported catalog")
		}
	}
	err := activate(context.Background(), domain.CatalogSnapshot{}, domain.CatalogSnapshot{Factors: []domain.FactorDef{{FactorID: "test", FactorType: domain.FactorTypeTimeSeries}}}, func() error { return nil })
	if err != nil || !called {
		t.Fatalf("TS activation: %v", err)
	}
}

func TestEngineExampleLoads(t *testing.T) {
	cfg, err := LoadEngineApplicationConfig("../../config/engine-app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cache.Enabled {
		t.Fatal("example enables unsupported cache")
	}
}

func TestControlExampleLoads(t *testing.T) {
	cfg, err := LoadControlConfig("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cfg.ArtifactsDir) == "" {
		t.Fatal("control example missing artifacts_dir")
	}
}
