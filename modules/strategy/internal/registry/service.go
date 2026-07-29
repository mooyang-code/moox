package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"gopkg.in/yaml.v3"
)

const APIVersionV1 = "moox.strategy/v1"

var ErrImmutableStrategy = errors.New("strategy: immutable artifact")

type Manifest struct {
	APIVersion string        `yaml:"api_version"`
	Entrypoint string        `yaml:"entrypoint"`
	Input      ManifestInput `yaml:"input"`
}

type ManifestInput struct {
	HistoryBars int `yaml:"history_bars"`
}

type Service struct {
	Repo *store.Store
	Now  func() time.Time
}

func Parse(raw string) (Manifest, error) {
	var manifest Manifest
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return manifest, errors.New("strategy manifest must contain one YAML document")
		}
		return manifest, err
	}
	if manifest.APIVersion != APIVersionV1 {
		return manifest, errors.New("strategy manifest api_version must be moox.strategy/v1")
	}
	if strings.TrimSpace(manifest.Entrypoint) == "" {
		return manifest, errors.New("strategy manifest entrypoint is required")
	}
	if manifest.Input.HistoryBars <= 0 {
		return manifest, errors.New("strategy manifest input.history_bars must be positive")
	}
	return manifest, nil
}

func (s *Service) Publish(
	ctx context.Context,
	strategyID, name, manifest, source string,
) (domain.Strategy, error) {
	strategy, err := s.Prepare(strategyID, name, manifest, source)
	if err != nil {
		return domain.Strategy{}, err
	}
	if err := s.Save(ctx, strategy); err != nil {
		return domain.Strategy{}, err
	}
	return strategy, nil
}

func (s *Service) Prepare(strategyID, name, manifest, source string) (domain.Strategy, error) {
	if strings.TrimSpace(strategyID) == "" || strings.TrimSpace(name) == "" {
		return domain.Strategy{}, errors.New("strategy id and name are required")
	}
	if _, err := Parse(manifest); err != nil {
		return domain.Strategy{}, err
	}
	if source == "" {
		return domain.Strategy{}, errors.New("strategy source is required")
	}
	sum := sha256.Sum256([]byte(source))
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	return domain.Strategy{
		ID: strategyID, Name: name, ManifestYAML: manifest, SourceCode: source,
		SourceHash: hex.EncodeToString(sum[:]), CreatedAt: now,
	}, nil
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
		left.SourceCode == right.SourceCode &&
		left.SourceHash == right.SourceHash
}
