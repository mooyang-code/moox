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
	FactorID        string
	InputColumns    []string
	Outputs         []string
	ParamsJSON      string
	LookbackPeriods int
}

// Service manages local factor definitions.
type Service struct {
	factors   *store.FactorRepository
	bindings  *store.BindingRepository
	meta      *MetadataSync
	opts      Options
	publisher *moduleregistry.SourcePublisher
}

// WithBindings adds binding persistence for lifecycle contract validation.
func (s *Service) WithBindings(bindings *store.BindingRepository) *Service {
	s.bindings = bindings
	return s
}

// SaveFactorDefinition persists a disabled definition after enforcing the same
// lifecycle rules used by CLI import and RPC updates.
func (s *Service) SaveFactorDefinition(ctx context.Context, factor domain.FactorDef) error {
	existing, err := s.validateDefinitionWrite(ctx, factor)
	if err != nil {
		return err
	}
	if existing == nil {
		return s.factors.Create(ctx, factor)
	}
	return s.factors.Update(ctx, factor)
}

func (s *Service) validateDefinitionWrite(ctx context.Context, factor domain.FactorDef) (*domain.FactorDef, error) {
	if s.factors == nil {
		return nil, fmt.Errorf("factor repository is required")
	}
	existing, err := s.factors.Get(ctx, factor.FactorID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if factor.Status != domain.FactorStatusDisabled {
			return nil, fmt.Errorf("new factor definitions must be disabled")
		}
		if byName, nameErr := s.factors.GetByName(ctx, factor.Name); nameErr == nil && byName.FactorID != factor.FactorID {
			return nil, fmt.Errorf("factor name %q already exists", factor.Name)
		} else if nameErr != nil && !errors.Is(nameErr, gorm.ErrRecordNotFound) {
			return nil, nameErr
		}
		return nil, nil
	}
	if existing.Status == domain.FactorStatusEnabled {
		return nil, fmt.Errorf("disable factor %q before updating its definition", factor.FactorID)
	}
	if factor.Status != domain.FactorStatusDisabled {
		return nil, fmt.Errorf("factor status must be changed through SetFactorStatus")
	}
	if existing.Name != factor.Name {
		return nil, fmt.Errorf("factor name is immutable; create a new factor_id")
	}
	if !slices.Equal(existing.Outputs, factor.Outputs) {
		return nil, fmt.Errorf("factor outputs are immutable; create a new factor_id")
	}
	return existing, nil
}

// ValidateEnabledBinding validates one binding through the configured Storage client.
func (s *Service) ValidateEnabledBinding(ctx context.Context, binding domain.FactorBinding, factor domain.FactorDef) error {
	if s.meta == nil {
		return fmt.Errorf("Storage metadata is required to enable binding %q", binding.BindingID)
	}
	return s.meta.ValidateEnabledBinding(ctx, binding, factor)
}

// ValidateCandidateBindingSet rejects an enabled binding set where a source is
// also produced as a target in the same space.
func (s *Service) ValidateCandidateBindingSet(bindings []domain.FactorBinding) error {
	return validateCandidateBindingSet(bindings)
}

// ValidateEnabledBindingsForFactor validates every binding that would become
// executable before any runtime metadata or local status is changed.
func (s *Service) ValidateEnabledBindingsForFactor(ctx context.Context, factor domain.FactorDef) error {
	if s.bindings == nil {
		return nil
	}
	bindings, err := s.bindings.ListByFactor(ctx, factor.FactorID)
	if err != nil {
		return err
	}
	if err := s.ValidateCandidateBindingSet(bindings); err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.Status != domain.BindingStatusEnabled {
			continue
		}
		if err := s.ValidateEnabledBinding(ctx, binding, factor); err != nil {
			return fmt.Errorf("binding %q: %w", binding.BindingID, err)
		}
	}
	return nil
}

// ValidateAllEnabledBindings fails closed when persisted executable state no
// longer satisfies the current binding and Storage View contracts.
func (s *Service) ValidateAllEnabledBindings(ctx context.Context) error {
	if s == nil || s.factors == nil {
		return fmt.Errorf("factor repository is required")
	}
	const pageSize = 1000
	for page := 1; ; page++ {
		factors, total, err := s.factors.List(ctx, store.FactorFilter{
			Status: domain.FactorStatusEnabled,
			Page:   store.Page{Page: page, PageSize: pageSize},
		})
		if err != nil {
			return fmt.Errorf("list enabled factors: %w", err)
		}
		for _, factor := range factors {
			if err := s.ValidateEnabledBindingsForFactor(ctx, factor); err != nil {
				return fmt.Errorf("factor %q: %w", factor.FactorID, err)
			}
		}
		if int64(page*pageSize) >= total {
			return nil
		}
	}
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
		FactorID:        options.FactorID,
		Name:            name,
		SourceCode:      string(raw),
		InputColumns:    options.InputColumns,
		Outputs:         options.Outputs,
		ParamsJSON:      options.ParamsJSON,
		LookbackPeriods: options.LookbackPeriods,
		Status:          domain.FactorStatusDisabled,
	}
	factor, err = domain.NormalizeFactorDefinition(factor)
	if err != nil {
		return nil, err
	}
	raw = []byte(factor.SourceCode)
	sum := sha256.Sum256(raw)
	factor.SourceHash = hex.EncodeToString(sum[:])
	if s.factors != nil {
		_, err = s.validateDefinitionWrite(ctx, factor)
		if err != nil {
			return nil, err
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
		if err = s.SaveFactorDefinition(ctx, factor); err != nil {
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
