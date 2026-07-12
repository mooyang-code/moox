package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"gopkg.in/yaml.v3"
	"io"
	"strings"
)

var ErrImmutableVersion = errors.New("strategy: immutable version")

type Manifest struct {
	ID                 string `yaml:"id"`
	Version            string `yaml:"version"`
	API                string `yaml:"api_version"`
	Entrypoint         string `yaml:"entrypoint"`
	StateSchemaVersion int    `yaml:"state_schema_version"`
}
type Service struct{ Repo *store.Store }

func Parse(raw string) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return m, errors.New("strategy manifest must contain one YAML document")
		}
		return m, err
	}
	if m.ID == "" || m.Version == "" || m.API == "" || m.Entrypoint == "" {
		return m, errors.New("strategy manifest requires id, version, api_version and entrypoint")
	}
	return m, nil
}
func (s *Service) Publish(ctx context.Context, manifest, source string) (domain.StrategyDefinition, error) {
	d, err := s.Prepare(manifest, source)
	if err != nil {
		return domain.StrategyDefinition{}, err
	}
	if err := s.Save(ctx, d); err != nil {
		return domain.StrategyDefinition{}, err
	}
	return d, nil
}

// Prepare validates and hashes a package without persisting it. Callers that
// have a runtime can LOAD the materialized source before Save, preventing the
// registry from acknowledging code the worker cannot import.
func (s *Service) Prepare(manifest, source string) (domain.StrategyDefinition, error) {
	m, err := Parse(manifest)
	if err != nil {
		return domain.StrategyDefinition{}, err
	}
	sum := sha256.Sum256([]byte(source))
	d := domain.StrategyDefinition{StrategyID: m.ID, Version: m.Version, API: m.API, ManifestYAML: manifest, SourceCode: source, SourceHash: hex.EncodeToString(sum[:]), StateSchemaVersion: m.StateSchemaVersion, Status: "draft"}
	return d, nil
}

func (s *Service) Save(ctx context.Context, d domain.StrategyDefinition) error {
	if s.Repo == nil {
		return nil
	}
	if err := s.Repo.SaveDefinition(ctx, d); err != nil {
		old, getErr := s.Repo.GetDefinition(ctx, d.StrategyID, d.Version)
		if getErr != nil {
			return err
		}
		if old.SourceHash != d.SourceHash || old.ManifestYAML != d.ManifestYAML || old.API != d.API || old.StateSchemaVersion != d.StateSchemaVersion {
			return ErrImmutableVersion
		}
		if old.Status != d.Status {
			// Validation is the only forward lifecycle transition performed by
			// this service: a successfully loaded draft may become enabled, but
			// an enabled version can never be downgraded by a retry.
			if old.Status == "draft" && d.Status == "enabled" {
				return s.Repo.EnableDefinition(ctx, d.StrategyID, d.Version)
			}
			return ErrImmutableVersion
		}
	}
	return nil
}
