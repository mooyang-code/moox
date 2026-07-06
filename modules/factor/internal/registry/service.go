package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/repository"
)

var factorFilePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*\.py$`)

// Options controls registry source import behavior.
type Options struct {
	FactorsDir    string
	DefaultParams []int
}

// Service manages local factor definitions.
type Service struct {
	factors *repository.FactorRepository
	meta    *MetadataSync
	opts    Options
}

// NewService creates a registry service.
func NewService(factors *repository.FactorRepository, meta *MetadataSync, opts Options) *Service {
	if opts.FactorsDir == "" {
		opts.FactorsDir = "./factors"
	}
	if len(opts.DefaultParams) == 0 {
		opts.DefaultParams = []int{20}
	}
	return &Service{factors: factors, meta: meta, opts: opts}
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
	params := append([]int(nil), s.opts.DefaultParams...)
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal default params: %w", err)
	}
	sum := sha256.Sum256(raw)
	factor := domain.FactorDef{
		FactorID:      strings.ToLower(name),
		Name:          name,
		Kind:          domain.FactorKindTimeseries,
		SourceCode:    string(raw),
		SourceHash:    hex.EncodeToString(sum[:]),
		ParamsJSON:    string(paramsJSON),
		LookbackBars:  DefaultLookback(params),
		WritebackBars: 5,
		DependsJSON:   DependsJSONFromSource(string(raw)),
		Status:        domain.FactorStatusEnabled,
	}
	if s.factors == nil {
		return &factor, nil
	}
	if err := s.factors.Upsert(ctx, factor); err != nil {
		return nil, err
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
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		return fmt.Errorf("write factor source %s: %w", target, err)
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
