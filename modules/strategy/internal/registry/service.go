package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/repository"
	"gopkg.in/yaml.v3"
)

var ErrImmutableVersion = errors.New("strategy: immutable version")

type Manifest struct {
	ID                 string `yaml:"id"`
	Version            string `yaml:"version"`
	API                string `yaml:"api_version"`
	Entrypoint         string `yaml:"entrypoint"`
	StateSchemaVersion int    `yaml:"state_schema_version"`
}
type Service struct{ Repo *repository.Repository }

func Parse(raw string) (Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal([]byte(raw), &m); err != nil {
		return m, err
	}
	if m.ID == "" || m.Version == "" || m.API == "" || m.Entrypoint == "" {
		return m, errors.New("strategy manifest requires id, version, api_version and entrypoint")
	}
	return m, nil
}
func (s *Service) Publish(ctx context.Context, manifest, source string) (domain.StrategyDefinition, error) {
	m, err := Parse(manifest)
	if err != nil {
		return domain.StrategyDefinition{}, err
	}
	sum := sha256.Sum256([]byte(source))
	d := domain.StrategyDefinition{StrategyID: m.ID, Version: m.Version, API: m.API, ManifestYAML: manifest, SourceCode: source, SourceHash: hex.EncodeToString(sum[:]), StateSchemaVersion: m.StateSchemaVersion, Status: "draft"}
	if s.Repo != nil {
		if err := s.Repo.SaveDefinition(ctx, d); err != nil {
			old, getErr := s.Repo.GetDefinition(ctx, m.ID, m.Version)
			if getErr != nil || old.SourceHash != d.SourceHash {
				return domain.StrategyDefinition{}, ErrImmutableVersion
			}
		}
	}
	return d, nil
}
