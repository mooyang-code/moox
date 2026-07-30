package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
)

func runImport(ctx context.Context, cfg cliConfig, out io.Writer) error {
	db, err := store.Open(&store.Options{Path: cfg.DBPath})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		return fmt.Errorf("apply factor schema: %w", err)
	}
	if cfg.File == "" {
		return fmt.Errorf("--file is required")
	}
	svc := registry.NewService(db.Factors(), nil, registry.Options{FactorsDir: cfg.FactorsDir})
	factor, err := svc.ImportFactorFile(ctx, cfg.File, registry.ImportOptions{
		FactorID: cfg.FactorID, InputColumns: cfg.InputColumns, Outputs: cfg.Outputs,
		ParamsJSON: cfg.ParamsJSON, LookbackPeriods: cfg.LookbackPeriods,
	})
	if err != nil {
		return err
	}
	type importedFactor struct {
		FactorID   string `json:"factor_id"`
		SourceHash string `json:"source_hash"`
	}
	imported := []importedFactor{{FactorID: factor.FactorID, SourceHash: factor.SourceHash}}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "imported": imported})
}
