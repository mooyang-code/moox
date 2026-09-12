package catalogsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
)

// PrepareArtifacts verifies the entire snapshot before publishing any source.
// It returns a local copy; only its caller may atomically activate the catalog
// after coordinating old tasks and output ownership.
func PrepareArtifacts(ctx context.Context, root string, snapshot domain.CatalogSnapshot) (domain.CatalogSnapshot, error) {
	if root == "" || snapshot.Revision <= 0 {
		return domain.CatalogSnapshot{}, fmt.Errorf("artifact root and positive catalog revision are required")
	}
	factors := make(map[string]bool, len(snapshot.Factors))
	names := make(map[string]bool, len(snapshot.Factors))
	for _, factor := range snapshot.Factors {
		if factor.FactorID == "" || factors[factor.FactorID] || factor.Name == "" || names[factor.Name] {
			return domain.CatalogSnapshot{}, fmt.Errorf("empty or duplicate factor identity")
		}
		factors[factor.FactorID], names[factor.Name] = true, true
		if err := domain.ValidateFactorType(factor.FactorType); err != nil {
			return domain.CatalogSnapshot{}, err
		}
		sum := sha256.Sum256([]byte(factor.SourceCode))
		if factor.SourceCode == "" || hex.EncodeToString(sum[:]) != factor.SourceHash {
			return domain.CatalogSnapshot{}, fmt.Errorf("factor %q source hash mismatch", factor.FactorID)
		}
	}
	bindings := make(map[string]bool, len(snapshot.Bindings))
	for _, binding := range snapshot.Bindings {
		if binding.BindingID == "" || binding.BindingGeneration == "" || bindings[binding.BindingID] || !factors[binding.FactorID] {
			return domain.CatalogSnapshot{}, fmt.Errorf("invalid binding identity or factor reference %q", binding.BindingID)
		}
		bindings[binding.BindingID] = true
	}
	local := snapshot
	local.Factors = append([]domain.FactorDef{}, snapshot.Factors...)
	local.Bindings = append([]domain.FactorBinding{}, snapshot.Bindings...)
	publisher := moduleregistry.NewSourcePublisher(root)
	for i := range local.Factors {
		factor := &local.Factors[i]
		version, err := publisher.Publish(ctx, moduleregistry.ModuleSource{Type: "factor", LogicalID: factor.Name, Source: []byte(factor.SourceCode)})
		if err != nil {
			return domain.CatalogSnapshot{}, err
		}
		factor.SourcePath = version.Path
	}
	return local, nil
}
