package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
)

type catalogEntry struct {
	File            string         `json:"file"`
	FactorID        string         `json:"factor_id"`
	InputColumns    []string       `json:"input_columns"`
	Outputs         []string       `json:"outputs"`
	Params          map[string]any `json:"params"`
	LookbackPeriods int            `json:"lookback_periods"`
}

// runImportCatalog imports the checked-in XBX catalog as disabled FactorDefs.
// Bindings and source Views remain environment-specific and are intentionally
// configured separately through FactorMgr.UpsertBinding.
func runImportCatalog(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.CatalogPath == "" {
		return fmt.Errorf("--catalog is required")
	}
	raw, err := os.ReadFile(cfg.CatalogPath)
	if err != nil {
		return fmt.Errorf("read factor catalog: %w", err)
	}
	var entries []catalogEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("decode factor catalog: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("factor catalog is empty")
	}
	// Validate the complete catalog before invoking the mutating single-factor
	// importer. This prevents an invalid later entry from leaving an earlier
	// entry partially committed.
	seenIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.File == "" || entry.FactorID == "" || len(entry.InputColumns) == 0 || len(entry.Outputs) == 0 || entry.LookbackPeriods < 1 {
			return fmt.Errorf("invalid factor catalog entry %q", entry.FactorID)
		}
		if _, exists := seenIDs[entry.FactorID]; exists {
			return fmt.Errorf("duplicate factor catalog entry %q", entry.FactorID)
		}
		seenIDs[entry.FactorID] = struct{}{}
		path := filepath.Join(cfg.FactorsDir, entry.File)
		if strings.ToLower(filepath.Ext(path)) != ".py" {
			return fmt.Errorf("factor %s must reference a .py source", entry.FactorID)
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read factor %s: %w", entry.FactorID, readErr)
		}
		params := entry.Params
		if params == nil {
			params = map[string]any{}
		}
		paramsJSON, marshalErr := json.Marshal(params)
		if marshalErr != nil {
			return fmt.Errorf("encode params for %s: %w", entry.FactorID, marshalErr)
		}
		factor, normalizeErr := domain.NormalizeFactorDefinition(domain.FactorDef{
			FactorID: entry.FactorID, Name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			SourceCode: string(raw), InputColumns: entry.InputColumns, Outputs: entry.Outputs,
			ParamsJSON: string(paramsJSON), LookbackPeriods: entry.LookbackPeriods,
			Status: domain.FactorStatusDisabled,
		})
		if normalizeErr != nil {
			return fmt.Errorf("validate factor %s: %w", entry.FactorID, normalizeErr)
		}
		if factor.SourceCode == "" {
			return fmt.Errorf("factor %s source is empty", entry.FactorID)
		}
	}
	batch := make([]registry.BatchImport, 0, len(entries))
	for _, entry := range entries {
		params := entry.Params
		if params == nil {
			params = map[string]any{}
		}
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encode params for %s: %w", entry.FactorID, err)
		}
		batch = append(batch, registry.BatchImport{
			Path: filepath.Join(cfg.FactorsDir, entry.File),
			Options: registry.ImportOptions{
				FactorID: entry.FactorID, InputColumns: entry.InputColumns, Outputs: entry.Outputs,
				ParamsJSON: string(paramsJSON), LookbackPeriods: entry.LookbackPeriods,
			},
		})
	}
	factorStore, err := store.Open(&store.Options{Path: cfg.DBPath})
	if err != nil {
		return fmt.Errorf("open factor database: %w", err)
	}
	defer factorStore.Close()
	if err := factorStore.ApplySchema(factorschema.AllSQL()); err != nil {
		return fmt.Errorf("apply factor schema: %w", err)
	}
	service := registry.NewService(factorStore.Factors(), nil, registry.Options{FactorsDir: cfg.FactorsDir})
	factors, err := service.ImportFactorFiles(ctx, batch)
	if err != nil {
		return fmt.Errorf("import factor catalog: %w", err)
	}
	results := make([]map[string]string, 0, len(factors))
	for _, factor := range factors {
		results = append(results, map[string]string{"factor_id": factor.FactorID, "source_hash": factor.SourceHash})
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "imported": results})
}
