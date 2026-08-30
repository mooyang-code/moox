package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

var ErrImmutableStrategy = errors.New("strategy: immutable artifact")

type Service struct {
	Repo *store.Store
	Now  func() time.Time
}

// PrepareCompiled creates an immutable StrategyDef from a compiled manifest.
func (s *Service) PrepareCompiled(strategyID, name, manifestYAML string, compiled compiler.CompiledStrategy) (domain.Strategy, error) {
	if strings.TrimSpace(strategyID) == "" || strings.TrimSpace(name) == "" {
		return domain.Strategy{}, errors.New("strategy id and name are required")
	}
	if compiled.Kind != config.Kind || compiled.APIVersion != config.APIVersion || len(compiled.CompiledJSON) == 0 {
		return domain.Strategy{}, errors.New("compiled strategy artifact is invalid")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	sum := sha256.Sum256([]byte(manifestYAML))
	return domain.Strategy{ID: strategyID, Name: name, ManifestYAML: manifestYAML,
		Kind: compiled.Kind, CompiledJSON: append([]byte(nil), compiled.CompiledJSON...), SourceHash: hex.EncodeToString(sum[:]), CreatedAt: now}, nil
}

// PrepareCoinSelection parses and compiles a v2 manifest before persistence.
func (s *Service) PrepareCoinSelection(ctx context.Context, strategyID, name, manifestYAML, spaceID string, compiler compiler.Compiler) (domain.Strategy, error) {
	manifest, err := config.Parse([]byte(manifestYAML))
	if err != nil {
		return domain.Strategy{}, err
	}
	compiled, err := compiler.Compile(ctx, manifest, spaceID)
	if err != nil {
		return domain.Strategy{}, err
	}
	return s.PrepareCompiled(strategyID, name, manifestYAML, compiled)
}

func (s *Service) Save(ctx context.Context, strategy domain.Strategy) error {
	if s.Repo == nil {
		return nil
	}
	if err := s.Repo.SaveStrategy(ctx, strategy); err != nil {
		existing, getErr := s.Repo.GetStrategy(ctx, strategy.ID)
		if getErr != nil {
			return err
		}
		if !sameArtifact(existing, strategy) {
			return ErrImmutableStrategy
		}
	}
	return nil
}

func sameArtifact(left, right domain.Strategy) bool {
	return left.ID == right.ID &&
		left.Name == right.Name &&
		left.ManifestYAML == right.ManifestYAML &&
		string(left.CompiledJSON) == string(right.CompiledJSON) &&
		left.Kind == right.Kind &&
		left.SourceHash == right.SourceHash
}
