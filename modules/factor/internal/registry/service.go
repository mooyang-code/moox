package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
	"gorm.io/gorm"
)

var factorFilePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*\.py$`)

// Options controls registry source import behavior.
type Options struct {
	FactorsDir string
}

type ImportOptions struct {
	FactorID     string
	InputColumns []string
	Outputs      []string
	ParamsJSON   string
	LookbackRows int
	Status       string
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
	return &Service{factors: factors, meta: meta, opts: opts, publisher: moduleregistry.NewSourcePublisher(filepath.Join(opts.FactorsDir, ".versions"))}
}

// ImportFactorFile imports one trusted Python factor source file into SQLite.
func (s *Service) ImportFactorFile(ctx context.Context, path string, options ImportOptions) (*domain.FactorDef, error) {
	name, err := factorNameFromPath(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read factor file %s: %w", path, err)
	}
	factor := domain.FactorDef{
		FactorID:     options.FactorID,
		Name:         name,
		SourceCode:   string(raw),
		InputColumns: options.InputColumns,
		Outputs:      options.Outputs,
		ParamsJSON:   options.ParamsJSON,
		LookbackRows: options.LookbackRows,
		Status:       options.Status,
	}
	factor, err = domain.NormalizeFactorDefinition(factor)
	if err != nil {
		return nil, err
	}
	raw = []byte(factor.SourceCode)
	sum := sha256.Sum256(raw)
	factor.SourceHash = hex.EncodeToString(sum[:])
	var existing *domain.FactorDef
	if s.factors != nil {
		existing, err = s.factors.Get(ctx, factor.FactorID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if existing != nil && !slices.Equal(existing.Outputs, factor.Outputs) {
			return nil, fmt.Errorf("factor outputs are immutable; create a new factor_id")
		}
	}
	if s.publisher != nil {
		version, err := s.publisher.Publish(ctx, moduleregistry.ModuleSource{Type: "factor", LogicalID: name, Source: raw})
		if err != nil {
			return nil, err
		}
		factor.SourcePath = version.Path
	}
	if s.factors != nil {
		if existing == nil {
			err = s.factors.Create(ctx, factor)
		} else {
			err = s.factors.Update(ctx, factor)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := s.writeSourceBack(name, raw); err != nil {
		return nil, err
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
