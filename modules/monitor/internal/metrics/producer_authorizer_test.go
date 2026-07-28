package metrics

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func TestCheckProducerAuthorizerRequiresMatchingNode(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	checks := manager.Repositories().Checks
	if err := checks.Create(context.Background(), &domain.Check{
		CheckID: "sysdeploy:node-a:moox_collector",
		Enabled: true,
		Source:  domain.CheckSourceSysDeploy,
	}); err != nil {
		t.Fatal(err)
	}

	authorizer := CheckProducerAuthorizer{Checks: checks}
	registered, err := authorizer.IsRegistered(context.Background(), "moox_collector", "node-a")
	if err != nil || !registered {
		t.Fatalf("matching producer registered=%v err=%v", registered, err)
	}
	registered, err = authorizer.IsRegistered(context.Background(), "moox_collector", "node-b")
	if err != nil || registered {
		t.Fatalf("wrong-node producer registered=%v err=%v", registered, err)
	}
}

func TestCheckProducerAuthorizerAllowsOnlyConfiguredExternalProducerWithNodeIdentity(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	authorizer := CheckProducerAuthorizer{
		Checks: manager.Repositories().Checks,
		ExternalProducers: map[string]struct{}{
			"moox_collector_scf": {},
		},
	}
	registered, err := authorizer.IsRegistered(context.Background(), "moox_collector_scf", "scf-node-a")
	if err != nil || !registered {
		t.Fatalf("SCF producer registered=%v err=%v", registered, err)
	}
	registered, err = authorizer.IsRegistered(context.Background(), "moox_collector_scf", "")
	if err != nil || registered {
		t.Fatalf("empty-node SCF producer registered=%v err=%v", registered, err)
	}
	registered, err = authorizer.IsRegistered(context.Background(), "unknown_external", "scf-node-a")
	if err != nil || registered {
		t.Fatalf("unknown external producer registered=%v err=%v", registered, err)
	}
}
