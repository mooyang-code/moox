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

// BatchImport describes one factor source in an atomic catalog import.
type BatchImport struct {
	Path    string
	Options ImportOptions
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

// ReconcileAllEnabledBindings validates persisted executable contracts and
// refreshes their managed Result Dataset/View metadata before event consumers
// start. A schema revision that is not active yet moves the binding back to
// pending_view so calculation cannot race the Result View rebuild.
func (s *Service) ReconcileAllEnabledBindings(ctx context.Context) error {
	if err := s.ValidateAllEnabledBindings(ctx); err != nil {
		return err
	}
	if s.bindings == nil || s.meta == nil {
		return nil
	}
	const pageSize = 1000
	for page := 1; ; page++ {
		factors, total, err := s.factors.List(ctx, store.FactorFilter{
			Status: domain.FactorStatusEnabled,
			Page:   store.Page{Page: page, PageSize: pageSize},
		})
		if err != nil {
			return fmt.Errorf("list enabled factors for metadata reconciliation: %w", err)
		}
		for _, factor := range factors {
			bindings, err := s.bindings.ListByFactor(ctx, factor.FactorID)
			if err != nil {
				return fmt.Errorf("list factor %q bindings: %w", factor.FactorID, err)
			}
			for _, binding := range bindings {
				if binding.Status != domain.BindingStatusEnabled {
					continue
				}
				ready, err := s.meta.SyncBindingViews(ctx, binding, []domain.FactorDef{factor})
				if err != nil {
					return fmt.Errorf("reconcile binding %q result metadata: %w", binding.BindingID, err)
				}
				if ready {
					continue
				}
				binding.Status = domain.BindingStatusPendingView
				if err := s.bindings.Upsert(ctx, binding); err != nil {
					return fmt.Errorf("mark binding %q pending_view: %w", binding.BindingID, err)
				}
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

// EnsureSourceArtifacts reconstructs immutable Python modules from the
// definitions stored in SQLite. The deploy directory is replaceable while the
// database is persistent, so source artifacts are derived startup state rather
// than deployment-owned files.
func (s *Service) EnsureSourceArtifacts(ctx context.Context) error {
	if s == nil || s.factors == nil || s.publisher == nil {
		return fmt.Errorf("factor source artifact dependencies are required")
	}
	const pageSize = 1000
	for page := 1; ; page++ {
		factors, total, err := s.factors.List(ctx, store.FactorFilter{
			Page: store.Page{Page: page, PageSize: pageSize},
		})
		if err != nil {
			return fmt.Errorf("list factor source artifacts: %w", err)
		}
		for _, factor := range factors {
			version, err := s.publisher.Publish(ctx, moduleregistry.ModuleSource{
				Type: "factor", LogicalID: factor.Name, Source: []byte(factor.SourceCode),
			})
			if err != nil {
				return fmt.Errorf("publish factor %q source artifact: %w", factor.FactorID, err)
			}
			if version.SourceHash != factor.SourceHash {
				return fmt.Errorf("factor %q source hash mismatch: stored=%s published=%s", factor.FactorID, factor.SourceHash, version.SourceHash)
			}
			if err := s.factors.UpdateSourceArtifact(ctx, factor.FactorID, factor.SourceHash, version.Path); err != nil {
				return fmt.Errorf("persist factor %q source artifact: %w", factor.FactorID, err)
			}
		}
		if int64(page*pageSize) >= total {
			return nil
		}
	}
}

// ImportFactorFile imports one trusted Python factor source file into SQLite.
func (s *Service) ImportFactorFile(ctx context.Context, path string, options ImportOptions) (*domain.FactorDef, error) {
	factor, raw, err := s.prepareFactorFile(path, options)
	if err != nil {
		return nil, err
	}
	if s.factors != nil {
		_, err = s.validateDefinitionWrite(ctx, factor)
		if err != nil {
			return nil, err
		}
	}
	if s.publisher != nil {
		version, err := s.publisher.Publish(ctx, moduleregistry.ModuleSource{Type: "factor", LogicalID: factor.Name, Source: raw})
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
	if err := s.writeSourceBack(factor.Name, raw); err != nil {
		return nil, err
	}
	return &factor, nil
}

// ImportFactorFiles atomically persists a catalog of disabled factor
// definitions. Source versions are staged before the database transaction;
// an unused immutable version is harmless if the transaction later fails.
// Runtime source files are written only after the transaction commits.
func (s *Service) ImportFactorFiles(ctx context.Context, batch []BatchImport) ([]domain.FactorDef, error) {
	if s == nil || s.factors == nil {
		return nil, fmt.Errorf("factor repository is required")
	}
	if len(batch) == 0 {
		return nil, fmt.Errorf("factor import batch is empty")
	}
	prepared := make([]domain.FactorDef, 0, len(batch))
	rawSources := make([][]byte, 0, len(batch))
	for _, item := range batch {
		factor, raw, err := s.prepareFactorFile(item.Path, item.Options)
		if err != nil {
			return nil, err
		}
		if s.publisher != nil {
			version, err := s.publisher.Publish(ctx, moduleregistry.ModuleSource{Type: "factor", LogicalID: factor.Name, Source: raw})
			if err != nil {
				return nil, err
			}
			factor.SourcePath = version.Path
		}
		prepared = append(prepared, factor)
		rawSources = append(rawSources, raw)
	}
	if err := s.factors.Transaction(ctx, func(txFactors *store.FactorRepository) error {
		txService := *s
		txService.factors = txFactors
		txService.publisher = nil
		txService.opts.FactorsDir = ""
		for _, factor := range prepared {
			if err := txService.SaveFactorDefinition(ctx, factor); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for index, factor := range prepared {
		if err := s.writeSourceBack(factor.Name, rawSources[index]); err != nil {
			return nil, err
		}
	}
	return prepared, nil
}

func (s *Service) prepareFactorFile(path string, options ImportOptions) (domain.FactorDef, []byte, error) {
	name, err := factorNameFromPath(path)
	if err != nil {
		return domain.FactorDef{}, nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return domain.FactorDef{}, nil, fmt.Errorf("read factor file %s: %w", path, err)
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
		return domain.FactorDef{}, nil, err
	}
	raw = []byte(factor.SourceCode)
	sum := sha256.Sum256(raw)
	factor.SourceHash = hex.EncodeToString(sum[:])
	return factor, raw, nil
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
