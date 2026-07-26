package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
)

var factorFilePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*\.py$`)

// Options controls registry source import behavior.
type Options struct {
	FactorsDir     string
	DefaultPeriods []int
}

// Service manages local factor definitions.
type Service struct {
	factors   *store.FactorRepository
	meta      *MetadataSync
	opts      Options
	publisher *moduleregistry.SourcePublisher
}

// NewService creates a registry service.
func NewService(factors *store.FactorRepository, meta *MetadataSync, opts Options) *Service {
	if opts.FactorsDir == "" {
		opts.FactorsDir = "./factors"
	}
	if len(opts.DefaultPeriods) == 0 {
		opts.DefaultPeriods = []int{20}
	}
	return &Service{factors: factors, meta: meta, opts: opts, publisher: moduleregistry.NewSourcePublisher(filepath.Join(opts.FactorsDir, ".versions"))}
}

// ImportFactorFile imports one trusted Python factor source file into SQLite.
func (s *Service) ImportFactorFile(ctx context.Context, path string) (*domain.FactorDef, error) {
	name, err := factorNameFromPath(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read factor file %s: %w", path, err)
	}
	periods := append([]int(nil), s.opts.DefaultPeriods...)
	sum := sha256.Sum256(raw)
	factor := domain.FactorDef{
		FactorID:     strings.ToLower(name),
		Name:         name,
		SourceCode:   string(raw),
		SourceHash:   hex.EncodeToString(sum[:]),
		Periods:      periods,
		LookbackBars: DefaultLookback(periods),
		Depends:      DependsFromSource(string(raw)),
		Status:       domain.FactorStatusEnabled,
	}
	factor, err = domain.NormalizeFactorDefinition(factor)
	if err != nil {
		return nil, err
	}
	if err := s.writeSourceBack(name, raw); err != nil {
		return nil, err
	}
	if s.publisher != nil {
		version, err := s.publisher.Publish(ctx, moduleregistry.ModuleSource{Type: "factor", LogicalID: name, Source: raw})
		if err != nil {
			return nil, err
		}
		factor.SourcePath = version.Path
	}
	if s.factors != nil {
		if err := s.factors.Upsert(ctx, factor); err != nil {
			return nil, err
		}
	}
	return &factor, nil
}

func (s *Service) writeSourceBack(name string, raw []byte) error {
	if s.opts.FactorsDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.opts.FactorsDir, 0o755); err != nil {
		return fmt.Errorf("create factors dir: %w", err)
	}
	target := filepath.Join(s.opts.FactorsDir, name+".py")
	tmp, err := os.CreateTemp(s.opts.FactorsDir, ".factor-*.py")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write factor source %s: %w", target, err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("publish factor source %s: %w", target, err)
	}
	return nil
}

func factorNameFromPath(path string) (string, error) {
	base := filepath.Base(path)
	if !factorFilePattern.MatchString(base) {
		return "", fmt.Errorf("invalid factor filename %q", base)
	}
	return strings.TrimSuffix(base, ".py"), nil
}
